/*
 * Copyright 2023- IBM Inc. All rights reserved
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
)

type MockRegistrationServer struct{}

func (m *MockRegistrationServer) Register(context.Context, *pluginapi.RegisterRequest) (*pluginapi.Empty, error) {
	return &pluginapi.Empty{}, nil
}

func init() {
	ReadSysFile = getNumaNodeIdMock
}

func getNumaNodeIdMock(string) (int64, error) {
	return 1, nil
}

func setupPluginDirsSocks(tmpDir string) (devicePluginDir string, kubeletSock string, s *grpc.Server, err error) {
	devicePluginDir = filepath.Join(tmpDir, "device-plugins")
	if err := unix.Mkdir(devicePluginDir, 0755); err != nil {
		return "", "", nil, err
	}
	kubeletSock = filepath.Join(devicePluginDir, "kubelet.sock")
	socket, err := net.Listen("unix", kubeletSock)
	s = grpc.NewServer()
	pluginapi.RegisterRegistrationServer(s, &MockRegistrationServer{})
	go s.Serve(socket)
	return devicePluginDir, kubeletSock, s, err
}

func setupDevFiles(tmpDir string, nrEfa int) (efaDirPath string, err error) {
	efaDirPath = filepath.Join(tmpDir, "infiniband")
	if err := unix.Mkdir(efaDirPath, 0755); err != nil {
		return "", nil
	}
	for i := 0; i < nrEfa; i++ {
		fd, err := unix.Open(fmt.Sprintf("%s/uverbs%d", efaDirPath, i), unix.O_CREAT|unix.O_EXCL, 0644)
		if err != nil {
			return "", err
		}
		unix.Close(fd)
	}
	return efaDirPath, nil
}

func TestDpLoop(t *testing.T) {
	tmpDir := t.TempDir()
	efaDirPath, err := setupDevFiles(tmpDir, 4)
	if err != nil {
		t.Errorf("Failed: TestDpLoop, setupDevFiles, tmpDir=%v, err=%v", tmpDir, err)
		return
	}
	devicePluginDir, kubeletSock, s, err := setupPluginDirsSocks(tmpDir)
	if err != nil {
		t.Errorf("Failed: TestDpLoop, setupPluginDirsSocks, tmpDir=%v, err=%v", tmpDir, err)
		return
	}
	var wg sync.WaitGroup
	var lastErr error
	wg.Add(1)
	go func() {
		lastErr = dpLoop(devicePluginDir, kubeletSock, efaDirPath)
		wg.Done()
	}()
	time.Sleep(time.Second)
	unix.Kill(unix.Getpid(), unix.SIGHUP)
	time.Sleep(time.Second)
	s.Stop()
	socket, err := net.Listen("unix", kubeletSock)
	if err != nil {
		t.Errorf("Failed: TestDpLoop, Listen, kubeletSock=%v, err=%v", kubeletSock, err)
		return
	}
	s = grpc.NewServer()
	pluginapi.RegisterRegistrationServer(s, &MockRegistrationServer{})
	go s.Serve(socket)
	time.Sleep(time.Second)
	unix.Kill(unix.Getpid(), unix.SIGINT)
	wg.Wait()
	s.Stop()

	lastErr = dpLoop(filepath.Join(tmpDir, "device-plugin-noexist"), kubeletSock, efaDirPath)
	if lastErr == nil {
		t.Errorf("Failed: TestDpLoop, dpLoop should return err for non-existent device-plugin-dir")
		return
	}
}

func TestListAndWatchAndAllocate(t *testing.T) {
	tmpDir := t.TempDir()
	efaDirPath, err := setupDevFiles(tmpDir, 4)
	if err != nil {
		t.Errorf("Failed: TestListAndWatchAndAllocate, setupDevFiles, tmpDir=%v, err=%v", tmpDir, err)
		return
	}
	devicePluginDir, kubeletSock, s, err := setupPluginDirsSocks(tmpDir)
	if err != nil {
		t.Errorf("Failed: TestListAndWatchAndAllocate, setupPluginDirsSocks, tmpDir=%v, err=%v", tmpDir, err)
		return
	}
	defer s.Stop()
	efa := NewEfaDevicePlugin(devicePluginDir, efaDirPath)
	if err := efa.Serve(kubeletSock); err != nil {
		t.Errorf("Failed: TestListAndWatchAndAllocate, efa.Serve, tmpDir=%v, err=%v", tmpDir, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*100)
	conn, err := grpc.DialContext(ctx, "unix://"+efa.socket, grpc.WithBlock(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer cancel()
	if err != nil {
		t.Errorf("Failed: TestListAndWatchAndAllocate, DialContext, socket=%v, err=%v", efa.socket, err)
		return
	}
	client := pluginapi.NewDevicePluginClient(conn)
	ret, err := client.ListAndWatch(ctx, &pluginapi.Empty{})
	if err != nil {
		t.Errorf("Failed: TestListAndWatchAndAllocate, client.ListAndWatch, socket=%v, err=%v", efa.socket, err)
		return
	}
	resp, err := ret.Recv()
	if err != nil {
		t.Errorf("Failed: TestListAndWatchAndAllocate, Recv, socket=%v, err=%v", efa.socket, err)
		return
	}
	if len(resp.Devices) != 4 {
		t.Errorf("Failed: TestListAndWatchAndAllocate, ListAndWatch returned incorrect device numbers, len(resp.Devices): expect=4, actual=%v", len(resp.Devices))
		return
	}
	devices := make([]string, 0)
	for i := 0; i < len(resp.Devices); i++ {
		devices = append(devices, resp.Devices[i].ID)
	}
	devices = append(devices, "malformed", "ocp-efa-gdrdrv-tyos-100")

	resp2, err := client.Allocate(ctx, &pluginapi.AllocateRequest{ContainerRequests: []*pluginapi.ContainerAllocateRequest{{DevicesIDs: devices}}})
	if err != nil {
		t.Errorf("Failed: TestListAndWatchAndAllocate, Allocate, socket=%v, err=%v", efa.socket, err)
		return
	}
	cresp := resp2.GetContainerResponses()
	if len(cresp) != 1 || len(cresp[0].Devices) != 4 {
		t.Errorf("Failed: TestListAndWatchAndAllocate, malformed ContainerResponses, len(cresp): expected=1, actual=%v, len(cresp[0].Devices): expected=4, actual=%v", len(cresp), len(cresp[0].Devices))
		return
	}
	NrEfa := 0
	for _, dev := range cresp[0].Devices {
		if dev.ContainerPath != dev.HostPath {
			t.Errorf("Failed: TestListAndWatchAndAllocate, malformed ContainerResponses, containerPath != hostPath, dev=%v", *dev)
			return
		}
		if strings.HasPrefix(dev.ContainerPath, fmt.Sprintf("%s/uverbs", efaDirPath)) {
			NrEfa += 1
		}
	}
	if NrEfa != 4 {
		t.Errorf("Failed: TestListAndWatchAndAllocate, malformed ContainerResponses, NrEfa != 4 (actual: %v), cresp=%v", NrEfa, *cresp[0])
		return
	}
	_, err = client.Allocate(ctx, &pluginapi.AllocateRequest{ContainerRequests: []*pluginapi.ContainerAllocateRequest{{DevicesIDs: []string{"malformed"}}}})
	if err == nil {
		t.Errorf("Failed: TestListAndWatchAndAllocate, Allocate should return err")
		return
	}

	f, err := os.Create(fmt.Sprintf("%s/dummy", efaDirPath))
	if err != nil {
		t.Errorf("Failed: TestListAndWatchAndAllocate, Create, err=%v", err)
		return
	}
	for i := 0; i < 4; i++ {
		if err := unix.Unlink(fmt.Sprintf("%s/uverbs%d", efaDirPath, i)); err != nil {
			t.Errorf("Failed: TestListAndWatchAndAllocate, Unlink, err=%v", err)
			return
		}
		f.Close()
		resp, err = ret.Recv()
		if err != nil {
			t.Errorf("Failed: TestListAndWatchAndAllocate, Recv, socket=%v, err=%v", efa.socket, err)
			return
		}
		if len(resp.Devices) != 3-i {
			t.Errorf("Failed: TestListAndWatchAndAllocate, ListAndWatch returned incorrect device numbers, len(resp.Devices): expect=%v, actual=%v", 3-i, len(resp.Devices))
			return
		}
	}

	_, err = client.Allocate(ctx, &pluginapi.AllocateRequest{ContainerRequests: []*pluginapi.ContainerAllocateRequest{{DevicesIDs: devices}}})
	if err == nil {
		t.Errorf("Failed: TestListAndWatchAndAllocate, Allocate should return error, err=%v", err)
		return
	}
	efa.Stop()
	efa.Stop() // try second Stop
	time.Sleep(time.Millisecond * 100)
}

func TestListAndWatchNoDev(t *testing.T) {
	tmpDir := t.TempDir()
	efaDirPath := filepath.Join(tmpDir, "infiniband")
	if err := unix.Mkdir(efaDirPath, 0755); err != nil {
		t.Errorf("Failed: TestListAndWatchNoDev, Mkdir, efaDirPath=%v, err=%v", efaDirPath, err)
		return
	}
	devicePluginDir, kubeletSock, s, err := setupPluginDirsSocks(tmpDir)
	if err != nil {
		t.Errorf("Failed: TestListAndWatchNoDev, setupPluginDirsSocks, tmpDir=%v, err=%v", tmpDir, err)
		return
	}
	defer s.Stop()
	efa := NewEfaDevicePlugin(devicePluginDir, efaDirPath)
	if err := efa.Serve(kubeletSock); err != nil {
		t.Errorf("Failed: TestListAndWatchNoDev, efa.Serve, tmpDir=%v, err=%v", tmpDir, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*100)
	conn, err := grpc.DialContext(ctx, "unix://"+efa.socket, grpc.WithBlock(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer cancel()
	if err != nil {
		t.Errorf("Failed: TestListAndWatchNoDev, DialContext, socket=%v, err=%v", efa.socket, err)
		return
	}
	client := pluginapi.NewDevicePluginClient(conn)
	ret, err := client.ListAndWatch(ctx, &pluginapi.Empty{})
	if err != nil {
		t.Errorf("Failed: TestListAndWatchNoDev, client.ListAndWatch, socket=%v, err=%v", efa.socket, err)
		return
	}
	resp, err := ret.Recv()
	if err != nil {
		t.Errorf("Failed: TestListAndWatchNoDev, Recv, socket=%v, err=%v", efa.socket, err)
		return
	}
	if len(resp.Devices) != 0 {
		t.Errorf("Failed: TestListAndWatch, ListAndWatch returned incorrect device numbers, len(resp.Devices): expect=0, actual=%v", len(resp.Devices))
		return
	}
	efa.Stop()
	time.Sleep(time.Millisecond * 100)
}

func TestListAndWatchNoEfa(t *testing.T) {
	tmpDir := t.TempDir()
	efaDirPath := filepath.Join(tmpDir, "infiniband")
	if err := unix.Mkdir(efaDirPath, 0755); err != nil {
		t.Errorf("Failed: TestListAndWatchNoEfa, Mkdir, efaDirPath=%v, err=%v", efaDirPath, err)
		return
	}
	devicePluginDir, kubeletSock, s, err := setupPluginDirsSocks(tmpDir)
	if err != nil {
		t.Errorf("Failed: TestListAndWatchNoEfa, setupPluginDirsSocks, tmpDir=%v, err=%v", tmpDir, err)
		return
	}
	defer s.Stop()
	efa := NewEfaDevicePlugin(devicePluginDir, efaDirPath)
	if err := efa.Serve(kubeletSock); err != nil {
		t.Errorf("Failed: TestListAndWatchNoEfa, efa.Serve, tmpDir=%v, err=%v", tmpDir, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*100)
	conn, err := grpc.DialContext(ctx, "unix://"+efa.socket, grpc.WithBlock(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer cancel()
	if err != nil {
		t.Errorf("Failed: TestListAndWatchNoEfa, DialContext, socket=%v, err=%v", efa.socket, err)
		return
	}
	client := pluginapi.NewDevicePluginClient(conn)
	ret, err := client.ListAndWatch(ctx, &pluginapi.Empty{})
	if err != nil {
		t.Errorf("Failed: TestListAndWatchNoEfa, client.ListAndWatch, socket=%v, err=%v", efa.socket, err)
		return
	}
	resp, err := ret.Recv()
	if err != nil {
		t.Errorf("Failed: TestListAndWatchNoEfa, Recv, socket=%v, err=%v", efa.socket, err)
		return
	}
	if len(resp.Devices) != 0 {
		t.Errorf("Failed: TestListAndWatchNoEfa, ListAndWatch returned incorrect device numbers, len(resp.Devices): expect=0, actual=%v", len(resp.Devices))
		return
	}
	devices := make([]string, 0)
	for i := 0; i < len(resp.Devices); i++ {
		devices = append(devices, resp.Devices[i].ID)
	}
	devices = append(devices, "malformed", "ocp-efa-gdrdrv-tyos-256")

	_, err = client.Allocate(ctx, &pluginapi.AllocateRequest{ContainerRequests: []*pluginapi.ContainerAllocateRequest{{DevicesIDs: devices}}})
	if err == nil {
		t.Errorf("Failed: TestListAndWatchAndAllocate, Allocate should fail, socket=%v", efa.socket)
		return
	}
	efa.Stop()
	time.Sleep(time.Millisecond * 100)
}

func TestOtherRpcs(t *testing.T) {
	tmpDir := t.TempDir()
	efaDirPath, err := setupDevFiles(tmpDir, 4)
	if err != nil {
		t.Errorf("Failed: TestOtherRpcs, setupDevFiles, tmpDir=%v, err=%v", tmpDir, err)
		return
	}
	devicePluginDir, kubeletSock, s, err := setupPluginDirsSocks(tmpDir)
	if err != nil {
		t.Errorf("Failed: TestOtherRpcs, setupPluginDirsSocks, tmpDir=%v, err=%v", tmpDir, err)
		return
	}
	defer s.Stop()
	efa := NewEfaDevicePlugin(devicePluginDir, efaDirPath)
	if err := efa.Serve(kubeletSock); err != nil {
		t.Errorf("Failed: TestOtherRpcs, efa.Serve, tmpDir=%v, err=%v", tmpDir, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*100)
	conn, err := grpc.DialContext(ctx, "unix://"+efa.socket, grpc.WithBlock(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer cancel()
	if err != nil {
		t.Errorf("Failed: TestOtherRpcs, DialContext, socket=%v, err=%v", efa.socket, err)
		return
	}
	client := pluginapi.NewDevicePluginClient(conn)
	ret, err := client.GetDevicePluginOptions(ctx, &pluginapi.Empty{})
	if err != nil {
		t.Errorf("Failed: TestOtherRpcs, GetDevicePluginOptions, socket=%v, err=%v", efa.socket, err)
		return
	}
	if ret.GetGetPreferredAllocationAvailable() || ret.GetPreStartRequired() {
		t.Errorf("Failed: TestOtherRpcs, GetDevicePluginOptions should return false for all options, socket=%v, err=%v", efa.socket, err)
		return
	}
	_, err = client.PreStartContainer(ctx, &pluginapi.PreStartContainerRequest{})
	if err != nil {
		t.Errorf("Failed: TestOtherRpcs, PreStartContainer, socket=%v, err=%v", efa.socket, err)
		return
	}
	_, err = client.GetPreferredAllocation(ctx, &pluginapi.PreferredAllocationRequest{})
	if err != nil {
		t.Errorf("Failed: TestOtherRpcs, GetPreferredAllocation, socket=%v, err=%v", efa.socket, err)
		return
	}
}

func TestGetNumaNodeId(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "numa")
	if _, err := readSysFile(filePath); !os.IsNotExist(err) {
		t.Errorf("Failed: TestGetNumaNodeId, should fail with a nonexistent file")
		return
	}
	if err := os.WriteFile(filePath, []byte("abcdefghijklmn"), 0644); err != nil {
		t.Errorf("Failed: TestGetNumaNodeId, WriteFile, filePath=%v, err=%v", filePath, err)
		return
	}
	if _, err := readSysFile(filePath); err == nil {
		t.Errorf("Failed: TestGetNumaNodeId, should fail with a malformed file")
		return
	}
	if err := os.WriteFile(filePath, []byte("10\n"), 0644); err != nil {
		t.Errorf("Failed: TestGetNumaNodeId, WriteFile, filePath=%v, err=%v", filePath, err)
		return
	}
	id, err := readSysFile(filePath)
	if err != nil {
		t.Errorf("Failed: TestGetNumaNodeId, err=%v", err)
		return
	}
	if id != 10 {
		t.Errorf("Failed: TestGetNumaNodeId, expected=10, actual=%v", id)
	}
}
