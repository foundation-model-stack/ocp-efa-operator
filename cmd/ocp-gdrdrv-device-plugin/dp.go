/*
 * Copyright 2023- IBM Inc. All rights reserved
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
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

const gdrdrvResourceName = "fms.io/gdrdrv"
const gdrdrvPath = "/dev/gdrdrv"
const maxGdrdrvUsers = 128

var sigChan = make(chan os.Signal, 1)
var Mknod = unix.Mknod

func init() {
	signal.Notify(sigChan, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
}

func getGdrdrvMajor(devicesFile string) (int, error) {
	f, err := os.Open(devicesFile)
	if err != nil {
		log.Printf("Failed: getGdrdrvMajor, Open, filePath=%v, err=%v", devicesFile, err)
		return -1, err
	}
	defer f.Close()
	r := regexp.MustCompile(`^\s*(\d+)\s+gdrdrv$`)
	scanner := bufio.NewScanner(f)
	major := -1
	for scanner.Scan() {
		result := r.FindAllStringSubmatch(scanner.Text(), -1)
		if len(result) > 0 && len(result[0]) > 1 {
			i, err := strconv.ParseInt(result[0][1], 10, 32)
			if err != nil {
				log.Printf("Failed: getGdrdrvMajor, ParseInt, str=%v, err=%v", result[0][1], err)
				return -1, err
			}
			major = int(i)
			break
		}
	}
	if major == -1 {
		return -1, unix.ENODEV
	}
	return major, nil
}

var createdDevFile = ""

func createGdrDrv(major int, targetPath string) error {
	if err := Mknod(targetPath, 0666|unix.S_IFCHR, int(unix.Mkdev(uint32(major), 0))); err != nil {
		if err == unix.EEXIST {
			log.Printf("createGdrDrv (already exist), targetPath=%v", targetPath)
			return nil
		}
		log.Printf("Failed: createGdrDrv, Mknod, targetPath=%v, major=%v, err=%v", targetPath, major, err)
		return err
	}
	createdDevFile = targetPath
	log.Printf("createGdrDrv, targetPath=%v, major=%v", targetPath, major)
	return nil
}

func deleteGdrDrv() error {
	if createdDevFile == "" {
		return nil
	}
	if err := unix.Unlink(createdDevFile); err != nil {
		if err == unix.ENOENT {
			log.Printf("deleteGdrDrv (already unlinked), createdDevFile=%s", createdDevFile)
			return nil
		}
		log.Printf("Failed: deleteGdrDrv, Unlink, createdDevFile=%v, err=%v", createdDevFile, err)
		return err
	}
	createdDevFile = ""
	log.Printf("deleteGdrDrv, devFile=%v", createdDevFile)
	return nil
}

func createGdrDrvLoop(devicesFile string, targetPath string) error {
	for i := 0; ; i++ {
		major, err := getGdrdrvMajor(devicesFile)
		if err == unix.ENODEV {
			select {
			case <-sigChan:
				return unix.EINTR
			default:
				if i == 0 {
					log.Printf("Wait for loading kernel module gdrdrv...")
				}
				time.Sleep(time.Second * 3)
			}
			continue
		} else if err != nil {
			return err
		}
		return createGdrDrv(major, targetPath)
	}
}

func dpLoop(devicePluginPath string, kubeletSock string, gdrdrvPath string) error {
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
	var gdp *GdrdrvDevicePlugin
	for {
		if restart {
			if gdp != nil {
				gdp.Stop()
			}
			gdp = NewGdrdrvDevicePlugin(devicePluginPath, gdrdrvPath)
			if err = gdp.Serve(kubeletSock); err != nil {
				log.Printf("Failed: gdp.Serve, err=%v", err)
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
				if gdp != nil {
					gdp.Stop()
				}
				return nil
			}
		}
	}
	return err
}

func main() {
	if err := createGdrDrvLoop("/proc/devices", gdrdrvPath); err == nil {
		dpLoop(pluginapi.DevicePluginPath, pluginapi.KubeletSocket, gdrdrvPath)
		deleteGdrDrv()
	}
}

type GdrdrvDevicePlugin struct {
	hostName   string
	socket     string
	gdrdrvPath string

	stop chan interface{}

	server *grpc.Server
}

func NewGdrdrvDevicePlugin(pluginDir string, gdrdrvPath string) *GdrdrvDevicePlugin {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	return &GdrdrvDevicePlugin{
		hostName:   hostname,
		socket:     filepath.Join(pluginDir, "ocp-gdrdrv.sock"),
		gdrdrvPath: gdrdrvPath,
		stop:       make(chan interface{}),
	}
}

func (dp *GdrdrvDevicePlugin) GetDeviceId(idx int) string {
	return fmt.Sprintf("ocp-gdrdrv-%s-%d", dp.hostName, idx)
}

func (dp *GdrdrvDevicePlugin) GetIdxFromDeviceId(id string) int {
	prefix := fmt.Sprintf("ocp-gdrdrv-%s-", dp.hostName)
	if !strings.HasPrefix(id, prefix) {
		return -1
	}
	idx, err := strconv.ParseInt(strings.TrimPrefix(id, prefix), 10, 32)
	if err != nil {
		return -1
	}
	return int(idx)
}

func (dp *GdrdrvDevicePlugin) DeleteSocket() error {
	if err := os.Remove(dp.socket); err != nil && !os.IsNotExist(err) {
		log.Printf("Failed: DeleteSocket, os.Remove, socket=%v, err=%v", dp.socket, err)
		return err
	}
	return nil
}

func (dp *GdrdrvDevicePlugin) Serve(kubeletSockPath string) error {
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
		ResourceName: gdrdrvResourceName,
		Options:      &pluginapi.DevicePluginOptions{PreStartRequired: false, GetPreferredAllocationAvailable: false},
	})
	if err != nil {
		log.Printf("Failed: Register, client.Register, srcName=%s, err=%v", gdrdrvResourceName, err)
		return err
	}
	return nil
}

func (dp *GdrdrvDevicePlugin) Stop() error {
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

func (dp *GdrdrvDevicePlugin) IsDeviceName(name string) bool {
	return name == dp.gdrdrvPath
}

func (dp *GdrdrvDevicePlugin) GetDevices() []*pluginapi.Device {
	_, err := os.Stat(dp.gdrdrvPath)
	if err != nil {
		log.Printf("Failed: GetDevices, no gdrdrv found, gdrdrvPath=%v, err=%v", dp.gdrdrvPath, err)
		return nil
	}
	devs := make([]*pluginapi.Device, 0)
	for i := 0; i < maxGdrdrvUsers; i++ {
		devs = append(devs, &pluginapi.Device{
			ID: dp.GetDeviceId(i), Health: pluginapi.Healthy,
		})
	}
	return devs
}

func (dp *GdrdrvDevicePlugin) ListAndWatch(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {
	inactiveReply := &pluginapi.ListAndWatchResponse{}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("Failed: ListAndWatch, NewWatcher, err=%v", err)
		return nil
	}
	defer watcher.Close()

	err = watcher.Add(filepath.Dir(dp.gdrdrvPath))
	if err != nil {
		log.Printf("Failed: ListAndWatch, watcher.Add (0), dir=%v, err=%v", filepath.Dir(dp.gdrdrvPath), err)
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

func (dp *GdrdrvDevicePlugin) Allocate(ctx context.Context, reqs *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	_, err := os.Stat(dp.gdrdrvPath)
	if err != nil {
		return nil, fmt.Errorf("Allocate: no gdrdrv device file found, gdrdrvPath=%v, err=%v", dp.gdrdrvPath, err)
	}

	var responses pluginapi.AllocateResponse
	var lastErr error
	var requestGdrdrv bool
	for _, req := range reqs.ContainerRequests {
		response := new(pluginapi.ContainerAllocateResponse)
		for _, id := range req.DevicesIDs {
			idx := dp.GetIdxFromDeviceId(id)
			if idx == -1 {
				lastErr = fmt.Errorf("GetIdxFromDeviceId, malformed id, id=%v", id)
				log.Print(lastErr.Error())
				continue
			}
			requestGdrdrv = true
			break
		}
		if requestGdrdrv {
			response.Devices = append(response.Devices, &pluginapi.DeviceSpec{
				ContainerPath: dp.gdrdrvPath, HostPath: dp.gdrdrvPath, Permissions: "rw",
			})
			responses.ContainerResponses = append(responses.ContainerResponses, response)
		}
	}
	if len(responses.ContainerResponses) == 0 {
		return nil, lastErr
	}
	log.Printf("Info: allocate responses=%v", responses)
	return &responses, nil
}
func (dp *GdrdrvDevicePlugin) GetDevicePluginOptions(context.Context, *pluginapi.Empty) (*pluginapi.DevicePluginOptions, error) {
	return &pluginapi.DevicePluginOptions{
		PreStartRequired:                false,
		GetPreferredAllocationAvailable: false,
	}, nil
}
func (dp *GdrdrvDevicePlugin) PreStartContainer(context.Context, *pluginapi.PreStartContainerRequest) (*pluginapi.PreStartContainerResponse, error) {
	return &pluginapi.PreStartContainerResponse{}, nil
}
func (dp *GdrdrvDevicePlugin) GetPreferredAllocation(context.Context, *pluginapi.PreferredAllocationRequest) (*pluginapi.PreferredAllocationResponse, error) {
	return &pluginapi.PreferredAllocationResponse{}, nil
}
