/*
 * Copyright 2023- IBM Inc. All rights reserved
 * SPDX-License-Identifier: Apache-2.0
 */

package main

import (
	"context"
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

const devicesStr = `Character devices:
  1 mem
  4 /dev/vc/0
  4 tty
  4 ttyS
  5 /dev/tty
  5 /dev/console
  5 /dev/ptmx
  7 vcs
 10 misc
 13 input
 29 fb
128 ptm
136 pts
162 raw
180 usb
188 ttyUSB
189 usb_device
195 nvidia-frontend
202 cpu/msr
203 cpu/cpuid
226 drm
231 infiniband_mad
231 infiniband_verbs
234 nvidia-nvlink
235 nvidia-caps
236 dimmctl
237 ndctl
238 aux
239 infiniband_mad
240 infiniband_verbs
241 nvme-generic
242 nvme
243 uio
244 ipmidev
245 hidraw
246 usbmon
247 bsg
248 watchdog
249 ptp
250 pps
251 rtc
252 dax
253 tpm
254 gpiochip
506 mmpmem
507 ss
508 trace
509 gdrdrv
510 nvidia-uvm
511 nvidia-nvswitch

Block devices:
  9 md
252 virtblk
253 device-mapper
254 mdp
259 blkext
`
const devicesStrNoGdrdrv = `Character devices:
  1 mem
  4 /dev/vc/0
  4 tty
  4 ttyS
  5 /dev/tty
  5 /dev/console
  5 /dev/ptmx
  7 vcs
 10 misc
 13 input
 29 fb
128 ptm
136 pts
162 raw
180 usb
188 ttyUSB
189 usb_device
195 nvidia-frontend
202 cpu/msr
203 cpu/cpuid
226 drm
231 infiniband_mad
231 infiniband_verbs
234 nvidia-nvlink
235 nvidia-caps
236 dimmctl
237 ndctl
238 aux
239 infiniband_mad
240 infiniband_verbs
241 nvme-generic
242 nvme
243 uio
244 ipmidev
245 hidraw
246 usbmon
247 bsg
248 watchdog
249 ptp
250 pps
251 rtc
252 dax
253 tpm
254 gpiochip
506 mmpmem
507 ss
508 trace
509 nvidia-uvm
510 nvidia-nvswitch

Block devices:
  9 md
252 virtblk
253 device-mapper
254 mdp
259 blkext
`

const devicesStrMalformed = `
34359738368 nvidia-nvswitch
`

type MockRegistrationServer struct{}

func (m *MockRegistrationServer) Register(context.Context, *pluginapi.RegisterRequest) (*pluginapi.Empty, error) {
	return &pluginapi.Empty{}, nil
}

func init() {
	Mknod = mknodMock
}

func mknodMock(path string, mode uint32, dev int) (err error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_EXCL, 0644)
	if err == nil {
		unix.Close(fd)
	}
	return err
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

func setupDevFiles(tmpDir string) (gdrdrv string, err error) {
	gdrdrv = filepath.Join(tmpDir, "gdrdrv")
	fd, err := unix.Open(gdrdrv, unix.O_CREAT|unix.O_EXCL, 0644)
	if err != nil {
		return "", err
	}
	unix.Close(fd)
	return gdrdrv, nil
}

func genDevicesFile(tmpDir string) (string, error) {
	devicesFile := filepath.Join(tmpDir, "proc-devices")
	return devicesFile, os.WriteFile(devicesFile, []byte(devicesStr), 0644)
}

func TestGetGdrdrvMajor(t *testing.T) {
	tmpDir := t.TempDir()
	devicesFile, err := genDevicesFile(tmpDir)
	if err != nil {
		t.Errorf("Failed: TestGetGdrdrvMajor, genDevicesFile, devicesFile=%v, err=%v", devicesFile, err)
		return
	}
	major, err := getGdrdrvMajor(devicesFile)
	if err != nil {
		t.Errorf("Failed: TestGetGdrdrvMajor, getGdrdrvMajor, devicesFile=%v, err=%v", devicesFile, err)
		return
	}
	if major != 509 {
		t.Errorf("Failed: TestGetGdrdrvMajor, getGdrdrvMajor returned incorrect major, expected=509, actual=%v, devicesFile=%v, err=%v", major, devicesFile, err)
	}

	devicesFile = filepath.Join(tmpDir, "proc-devices-nogdrdrv")
	if err := os.WriteFile(devicesFile, []byte(devicesStrNoGdrdrv), 0644); err != nil {
		t.Errorf("Failed: TestGetGdrdrvMajor, os.WriteFile, devicesFile=%v, err=%v", devicesFile, err)
		return
	}
	_, err = getGdrdrvMajor(devicesFile)
	if err == nil {
		t.Errorf("Failed: TestGetGdrdrvMajor, getGdrdrvMajor should fail, devicesFile=%v, err=%v", devicesFile, err)
		return
	}
	devicesFile = filepath.Join(tmpDir, "proc-devices-malformed")
	if err := os.WriteFile(devicesFile, []byte(devicesStrMalformed), 0644); err != nil {
		t.Errorf("Failed: TestGetGdrdrvMajor, os.WriteFile, devicesFile=%v, err=%v", devicesFile, err)
		return
	}
	_, err = getGdrdrvMajor(devicesFile)
	if err == nil {
		t.Errorf("Failed: TestGetGdrdrvMajor, getGdrdrvMajor should fail, devicesFile=%v, err=%v", devicesFile, err)
		return
	}
	_, err = getGdrdrvMajor(filepath.Join(tmpDir, "noexist"))
	if err == nil {
		t.Errorf("Failed: TestGetGdrdrvMajor, getGdrdrvMajor should fail, devicesFile=%v, err=%v", devicesFile, err)
		return
	}
}

func TestCreateGdrdrv(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "dummy")
	if err := createGdrDrv(384, filePath); err != nil {
		t.Errorf("Failed: TestCreateGdrdrv, filePath=%v, err=%v", filePath, err)
		return
	}
	if err := createGdrDrv(384, filePath); err != nil {
		t.Errorf("Failed: TestCreateGdrdrv, the second createGdrDrv should ignore EEXIST, filePath=%v, err=%v", filePath, err)
		return
	}
	Mknod = func(path string, mode uint32, dev int) (err error) {
		return unix.EPERM
	}
	if err := createGdrDrv(384, filePath); err != unix.EPERM {
		t.Errorf("Failed: TestCreateGdrdrv, createGdrDrv should return unix.EPERM, filePath=%v, err=%v", filePath, err)
	}
	Mknod = mknodMock
}

func TestCreateGdrdrvLoop(t *testing.T) {
	tmpDir := t.TempDir()
	devicesFile := filepath.Join(tmpDir, "proc-devices")
	if err := os.WriteFile(devicesFile, []byte(devicesStrNoGdrdrv), 0644); err != nil {
		t.Errorf("Failed: TestCreateGdrdrvLoop, genDevicesFile, devicesFile=%v, err=%v", devicesFile, err)
		return
	}
	targetPath := filepath.Join(tmpDir, "gdrdrv")
	var wg sync.WaitGroup
	wg.Add(1)
	var lastErr error
	go func() {
		lastErr = createGdrDrvLoop(devicesFile, targetPath)
		wg.Done()
	}()
	time.Sleep(time.Millisecond * 500)
	if err := os.WriteFile(devicesFile, []byte(devicesStr), 0644); err != nil {
		t.Errorf("Failed: TestCreateGdrdrvLoop, genDevicesFile, devicesFile=%v, err=%v", devicesFile, err)
		return
	}
	time.Sleep(time.Second * 3)
	wg.Wait()
	if lastErr != nil {
		t.Errorf("Failed: TestCreateGdrdrvLoop, createGdrDrvLoop, err=%v", lastErr)
		return
	}

	devicesFile = filepath.Join(tmpDir, "proc-devices-2")
	if err := os.WriteFile(devicesFile, []byte(devicesStrNoGdrdrv), 0644); err != nil {
		t.Errorf("Failed: TestCreateGdrdrvLoop, genDevicesFile, devicesFile=%v, err=%v", devicesFile, err)
		return
	}
	targetPath = filepath.Join(tmpDir, "gdrdrv-2")
	wg.Add(1)
	go func() {
		lastErr = createGdrDrvLoop(devicesFile, targetPath)
		wg.Done()
	}()
	unix.Kill(unix.Getpid(), unix.SIGINT)

	devicesFile = filepath.Join(tmpDir, "proc-devices-3")
	err := createGdrDrvLoop(devicesFile, targetPath)
	if err == nil {
		t.Errorf("Failed: TestCreateGdrdrvLoop, createGdrDrvLoop should return no devicesFile, devicesFile=%v, err=%v", devicesFile, err)
	}
}

func TestDpLoop(t *testing.T) {
	tmpDir := t.TempDir()
	gdrdrvPath, err := setupDevFiles(tmpDir)
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
		lastErr = dpLoop(devicePluginDir, kubeletSock, gdrdrvPath)
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

	lastErr = dpLoop(filepath.Join(tmpDir, "device-plugin-noexist"), kubeletSock, gdrdrvPath)
	if lastErr == nil {
		t.Errorf("Failed: TestDpLoop, dpLoop should return err for non-existent device-plugin-dir")
		return
	}
}

func TestListAndWatchAndAllocate(t *testing.T) {
	tmpDir := t.TempDir()
	gdrdrvPath, err := setupDevFiles(tmpDir)
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
	efa := NewGdrdrvDevicePlugin(devicePluginDir, gdrdrvPath)
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
	if len(resp.Devices) != 128 {
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
	if len(cresp) != 1 || len(cresp[0].Devices) != 1 {
		t.Errorf("Failed: TestListAndWatchAndAllocate, malformed ContainerResponses, len(cresp): expected=1, actual=%v, len(cresp[0].Devices): expected=1, actual=%v", len(cresp), len(cresp[0].Devices))
		return
	}
	NrGdrdrv := 0
	for _, dev := range cresp[0].Devices {
		if dev.ContainerPath != dev.HostPath {
			t.Errorf("Failed: TestListAndWatchAndAllocate, malformed ContainerResponses, containerPath != hostPath, dev=%v", *dev)
			return
		}
		if strings.HasSuffix(dev.ContainerPath, "gdrdrv") {
			NrGdrdrv += 1
		}
	}
	if NrGdrdrv != 1 {
		t.Errorf("Failed: TestListAndWatchAndAllocate, malformed ContainerResponses, NrGdrdrv != 1 (actual: %v) cresp=%v", NrGdrdrv, *cresp[0])
		return
	}
	_, err = client.Allocate(ctx, &pluginapi.AllocateRequest{ContainerRequests: []*pluginapi.ContainerAllocateRequest{{DevicesIDs: []string{"malformed"}}}})
	if err == nil {
		t.Errorf("Failed: TestListAndWatchAndAllocate, Allocate should return err")
		return
	}

	if err := unix.Unlink(gdrdrvPath); err != nil {
		t.Errorf("Failed: TestListAndWatchAndAllocate, Unlink, gdrdrvPath=%v, err=%v", gdrdrvPath, err)
		return
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
	devicePluginDir, kubeletSock, s, err := setupPluginDirsSocks(tmpDir)
	if err != nil {
		t.Errorf("Failed: TestListAndWatchNoDev, setupPluginDirsSocks, tmpDir=%v, err=%v", tmpDir, err)
		return
	}
	defer s.Stop()
	efa := NewGdrdrvDevicePlugin(devicePluginDir, gdrdrvPath)
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
	gdrdrvPath, err := setupDevFiles(tmpDir)
	if err != nil {
		t.Errorf("Failed: TestListAndWatchNoEfa, setupDevFiles, tmpDir=%v, err=%v", tmpDir, err)
		return
	}
	devicePluginDir, kubeletSock, s, err := setupPluginDirsSocks(tmpDir)
	if err != nil {
		t.Errorf("Failed: TestListAndWatchNoEfa, setupPluginDirsSocks, tmpDir=%v, err=%v", tmpDir, err)
		return
	}
	defer s.Stop()
	efa := NewGdrdrvDevicePlugin(devicePluginDir, gdrdrvPath)
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
	if len(resp.Devices) != 128 {
		t.Errorf("Failed: TestListAndWatchNoEfa, ListAndWatch returned incorrect device numbers, len(resp.Devices): expect=128, actual=%v", len(resp.Devices))
		return
	}
	devices := make([]string, 0)
	for i := 0; i < len(resp.Devices); i++ {
		devices = append(devices, resp.Devices[i].ID)
	}
	devices = append(devices, "malformed", "ocp-efa-gdrdrv-tyos-256")

	resp2, err := client.Allocate(ctx, &pluginapi.AllocateRequest{ContainerRequests: []*pluginapi.ContainerAllocateRequest{{DevicesIDs: devices}}})
	if err != nil {
		t.Errorf("Failed: TestListAndWatchAndAllocate, Allocate, socket=%v, err=%v", efa.socket, err)
		return
	}
	cresp := resp2.GetContainerResponses()
	if len(cresp) != 1 || len(cresp[0].Devices) != 1 {
		t.Errorf("Failed: TestListAndWatchAndAllocate, malformed ContainerResponses, len(cresp): expected=1, actual=%v, len(cresp[0].Devices): expected=1, actual=%v", len(cresp), len(cresp[0].Devices))
		return
	}
	efa.Stop()
	time.Sleep(time.Millisecond * 100)
}

func TestOtherRpcs(t *testing.T) {
	tmpDir := t.TempDir()
	gdrdrvPath, err := setupDevFiles(tmpDir)
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
	efa := NewGdrdrvDevicePlugin(devicePluginDir, gdrdrvPath)
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
