/*
 * Copyright 2023- IBM Inc. All rights reserved
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

const efaResourceName = "fms.io/efa"
const efaDirPath = "/dev/infiniband"

var ReadSysFile = readSysFile

func dpLoop(devicePluginPath string, kubeletSock string, efaDriPath string) error {
	var sigChan = make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("Failed: NewWatcher, err=%v", err)
		return err
	}
	defer watcher.Close()

	err = watcher.Add(devicePluginPath)
	if err != nil {
		log.Printf("Failed: watcher.Add, devicePluginPath=%v, err=%v", devicePluginPath, err)
		return err
	}

	restart := true
	var dp *EfaDevicePlugin
	for {
		if restart {
			if dp != nil {
				dp.Stop()
			}
			dp = NewEfaDevicePlugin(devicePluginPath, efaDriPath)
			if err = dp.Serve(kubeletSock); err != nil {
				log.Printf("Failed: dp.Serve, err=%v", err)
				break
			}
			restart = false
		}
		select {
		case event := <-watcher.Events:
			if event.Name == kubeletSock && event.Op&fsnotify.Create == fsnotify.Create {
				restart = true
			}
		case err = <-watcher.Errors:
			log.Printf("watcher.Errors=%v", err)
		case s := <-sigChan:
			switch s {
			case unix.SIGHUP:
				restart = true
			case unix.SIGINT:
				fallthrough
			case unix.SIGTERM:
				fallthrough
			case unix.SIGQUIT:
				if dp != nil {
					dp.Stop()
				}
				return nil
			}
		}
	}
	return err
}

func main() {
	dpLoop(pluginapi.DevicePluginPath, pluginapi.KubeletSocket, efaDirPath)
}

type EfaDevicePlugin struct {
	hostName   string
	socket     string
	efaDirPath string

	stop chan interface{}

	server *grpc.Server
}

func NewEfaDevicePlugin(pluginDir string, efaDirPath string) *EfaDevicePlugin {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	return &EfaDevicePlugin{
		hostName:   hostname,
		socket:     filepath.Join(pluginDir, "ocp-efa.sock"),
		efaDirPath: efaDirPath,
		stop:       make(chan interface{}),
	}
}

func (dp *EfaDevicePlugin) GetDeviceId(idx int) string {
	return fmt.Sprintf("ocp-efa-%s-%d", dp.hostName, idx)
}

func (dp *EfaDevicePlugin) GetIdxFromDeviceId(id string) int {
	prefix := fmt.Sprintf("ocp-efa-%s-", dp.hostName)
	if !strings.HasPrefix(id, prefix) {
		return -1
	}
	idx, err := strconv.ParseInt(strings.TrimPrefix(id, prefix), 10, 32)
	if err != nil {
		return -1
	}
	return int(idx)
}

func (dp *EfaDevicePlugin) DeleteSocket() error {
	if err := os.Remove(dp.socket); err != nil && !os.IsNotExist(err) {
		log.Printf("Failed: DeleteSocket, os.Remove, socket=%v, err=%v", dp.socket, err)
		return err
	}
	return nil
}

func (dp *EfaDevicePlugin) Serve(kubeletSockPath string) error {
	if err := dp.DeleteSocket(); err != nil {
		return err
	}

	sock, err := net.Listen("unix", dp.socket)
	if err != nil {
		log.Printf("Failed: net.Listen, socket=%v, err=%v", dp.socket, err)
		return err
	}
	dp.server = grpc.NewServer([]grpc.ServerOption{}...)
	pluginapi.RegisterDevicePluginServer(dp.server, dp)
	go func() {
		if err := dp.server.Serve(sock); err != nil {
			log.Printf("Failed: Serve(), err=%v", err)
		}
	}()
	time.Sleep(time.Second)

	// Wait for server to start by launching a blocking connexion
	ctx2, cancel2 := context.WithTimeout(context.TODO(), 5*time.Second)
	conn2, err2 := grpc.DialContext(ctx2, "unix://"+dp.socket, grpc.WithBlock(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	cancel2()
	if err2 != nil {
		log.Printf("Failed: Register, grpc.Dial (0), socket=%v, err=%v", dp.socket, err2)
		return err2
	}
	conn2.Close()

	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(ctx, "unix://"+kubeletSockPath, grpc.WithBlock(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Printf("Failed: Register, grpc.Dial (1), kubeletSockPath=%v, err=%v", kubeletSockPath, err)
		return err
	}
	defer conn.Close()

	client := pluginapi.NewRegistrationClient(conn)
	_, err = client.Register(ctx, &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     path.Base(dp.socket),
		ResourceName: efaResourceName,
		Options:      &pluginapi.DevicePluginOptions{PreStartRequired: false, GetPreferredAllocationAvailable: false},
	})
	if err != nil {
		log.Printf("Failed: Register, client.Register, srcName=%s, err=%v", efaResourceName, err)
		return err
	}
	return nil
}

func (dp *EfaDevicePlugin) Stop() error {
	if dp.server == nil {
		return nil
	}
	dp.server.Stop()
	dp.server = nil
	if dp.stop != nil {
		close(dp.stop)
	}

	err := dp.DeleteSocket()
	log.Printf("Stop")
	return err
}

func (dp *EfaDevicePlugin) IsDeviceName(name string) bool {
	return strings.HasPrefix(name, fmt.Sprintf("%s/uverbs", dp.efaDirPath))
}

func readSysFile(filePath string) (int64, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return -1, err
	}
	defer f.Close()
	buf, err := io.ReadAll(f)
	if err != nil {
		return -1, err
	}
	ret, err := strconv.ParseInt(strings.TrimSuffix(string(buf), "\n"), 0, 64)
	if err != nil {
		return -1, err
	}
	return ret, nil
}

func (dp *EfaDevicePlugin) GetDevices() []*pluginapi.Device {
	devs := make([]*pluginapi.Device, 0)
	dirs, err := os.ReadDir(dp.efaDirPath)
	if err != nil {
		log.Printf("Failed: GetDevices, no efaDirPath dir found, efaDirPath=%v, err=%v", dp.efaDirPath, err)
		return nil
	}

	for _, entry := range dirs {
		if !strings.HasPrefix(entry.Name(), "uverbs") {
			continue
		}
		idx, err := strconv.ParseInt(strings.TrimPrefix(entry.Name(), "uverbs"), 10, 32)
		if err != nil {
			log.Printf("Failed: ParseInt, entryName=%v, err=%v", entry.Name(), err)
			return nil
		}
		numaId, err := ReadSysFile(fmt.Sprintf("/sys/class/infiniband/efa_%d/device/numa_node", idx))
		var topo *pluginapi.TopologyInfo = nil
		if err == nil {
			topo = &pluginapi.TopologyInfo{Nodes: []*pluginapi.NUMANode{{ID: numaId}}}
		}
		devs = append(devs, &pluginapi.Device{
			ID: dp.GetDeviceId(int(idx)), Health: pluginapi.Healthy, Topology: topo,
		})
	}
	if len(devs) == 0 {
		log.Printf("Failed: GetDevices, no uverbs found, efaDirPath=%v", dp.efaDirPath)
		return nil
	}
	return devs
}

func (dp *EfaDevicePlugin) ListAndWatch(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {
	inactiveReply := &pluginapi.ListAndWatchResponse{}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("Failed: ListAndWatch, NewWatcher, err=%v", err)
		return nil
	}
	defer watcher.Close()

	err = watcher.Add(dp.efaDirPath)
	if err != nil {
		log.Printf("Failed: ListAndWatch, watcher.Add (1), dir=%v, err=%v", dp.efaDirPath, err)
		return nil
	}

	devs := dp.GetDevices()
	if devs == nil {
		if err := s.Send(inactiveReply); err != nil {
			log.Printf("Failed: Send (1), inactiveReply=%v err=%v", inactiveReply, err)
			return err
		}
		log.Printf("Init: report no devices")
	} else {
		activeReply := &pluginapi.ListAndWatchResponse{Devices: devs}
		if err := s.Send(activeReply); err != nil {
			log.Printf("Failed: Send (2), activeReply=%v err=%v", activeReply, err)
			return err
		}
		log.Printf("Init: report %v devices, devs=%v", len(devs), devs)
	}
	for {
		select {
		case <-dp.stop:
			return nil
		case ev := <-watcher.Events:
			if !dp.IsDeviceName(ev.Name) {
				continue
			}
			devs := dp.GetDevices()
			if devs == nil {
				if err := s.Send(inactiveReply); err != nil {
					log.Printf("Failed: Send (3), inactiveReply=%v err=%v", inactiveReply, err)
					return err
				}
				log.Printf("Update: report no devices")
			} else {
				activeReply := &pluginapi.ListAndWatchResponse{Devices: devs}
				if err := s.Send(activeReply); err != nil {
					log.Printf("Failed: Send (4), activeReply=%v err=%v", activeReply, err)
					return err
				}
				log.Printf("Update: report %d devices, devs=%v", len(devs), devs)
			}
		}
	}
}

func (dp *EfaDevicePlugin) Allocate(ctx context.Context, reqs *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	var responses pluginapi.AllocateResponse
	var lastErr error
	for _, req := range reqs.ContainerRequests {
		response := new(pluginapi.ContainerAllocateResponse)
		for _, id := range req.DevicesIDs {
			idx := dp.GetIdxFromDeviceId(id)
			if idx == -1 {
				lastErr = fmt.Errorf("GetIdxFromDeviceId, malformed id, id=%v", id)
				log.Print(lastErr.Error())
				continue
			}
			uvPath := fmt.Sprintf("%s/%s", dp.efaDirPath, fmt.Sprintf("uverbs%d", idx))
			if _, err := os.Stat(uvPath); err != nil {
				lastErr = fmt.Errorf("Allocate: no uverbs device file found, uvPath=%v, err=%v", uvPath, err)
				log.Print(lastErr.Error())
				continue
			}
			response.Devices = append(response.Devices, &pluginapi.DeviceSpec{
				ContainerPath: uvPath, HostPath: uvPath, Permissions: "rw",
			})
		}
		if len(response.Devices) > 0 {
			responses.ContainerResponses = append(responses.ContainerResponses, response)
		}
	}
	if len(responses.ContainerResponses) == 0 {
		return nil, lastErr
	}
	log.Printf("Info: allocate responses=%v", responses)
	return &responses, nil
}
func (dp *EfaDevicePlugin) GetDevicePluginOptions(context.Context, *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{
		PreStartRequired:                false,
		GetPreferredAllocationAvailable: false,
	}, nil
}
func (dp *EfaDevicePlugin) PreStartContainer(context.Context, *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}
func (dp *EfaDevicePlugin) GetPreferredAllocation(context.Context, *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	return &pluginapi.PreferredAllocationResponse{}, nil
}
