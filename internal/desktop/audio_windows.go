//go:build windows

package desktop

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// wasapiAudio records the default output device with WASAPI in loopback
// mode: what the speakers play, in the device's own mix format. All COM
// calls run on one locked OS thread.
type wasapiAudio struct {
	rate, channels int
	ch             chan []int16
	stop           atomic.Bool
	done           chan struct{}
	err            error // why ch closed
	once           sync.Once
}

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")

	clsidMMDeviceEnumerator = windows.GUID{Data1: 0xBCDE0395, Data2: 0xE52F, Data3: 0x467C, Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator  = windows.GUID{Data1: 0xA95664D2, Data2: 0x9614, Data3: 0x4F35, Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	iidIAudioClient         = windows.GUID{Data1: 0x1CB9AD4C, Data2: 0xDBFA, Data3: 0x4C32, Data4: [8]byte{0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2}}
	iidIAudioCaptureClient  = windows.GUID{Data1: 0xC8ADBD64, Data2: 0xE71E, Data3: 0x48A0, Data4: [8]byte{0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17}}
)

const (
	clsctxAll               = 0x17
	eRender                 = 0
	eConsole                = 0
	audclntShareModeShared  = 0
	audclntStreamLoopback   = 0x00020000
	audclntBufferSilent     = 0x2
	wasapiBuffer            = 200 * time.Millisecond
	wasapiPoll              = 10 * time.Millisecond
	waveFormatPCM           = 1
	waveFormatIEEEFloat     = 3
	waveFormatExtensible    = 0xFFFE
	maxWireRate             = 48000 // higher device rates are decimated to this or below
	coinitMultithreaded     = 0x0
	vtIUnknownRelease       = 2
	vtEnumGetDefault        = 4  // IMMDeviceEnumerator::GetDefaultAudioEndpoint
	vtDeviceActivate        = 3  // IMMDevice::Activate
	vtClientInitialize      = 3  // IAudioClient::Initialize
	vtClientGetMixFormat    = 8  // IAudioClient::GetMixFormat
	vtClientStart           = 10 // IAudioClient::Start
	vtClientStop            = 11 // IAudioClient::Stop
	vtClientGetService      = 14 // IAudioClient::GetService
	vtCaptureGetBuffer      = 3  // IAudioCaptureClient::GetBuffer
	vtCaptureReleaseBuffer  = 4  // IAudioCaptureClient::ReleaseBuffer
	vtCaptureGetNextPacket  = 5  // IAudioCaptureClient::GetNextPacketSize
	waveFormatExSize        = 18
	waveFormatExtensibleLen = 40
)

// comCall calls method i of the COM object obj.
func comCall(obj uintptr, i int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(ptr(obj))
	fn := *(*uintptr)(ptr(vtbl + uintptr(i)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

func comRelease(obj uintptr) {
	if obj != 0 {
		comCall(obj, vtIUnknownRelease)
	}
}

func hresult(what string, hr uintptr) error {
	if int32(hr) < 0 {
		return fmt.Errorf("%s: %w", what, syscall.Errno(hr))
	}
	return nil
}

// mixFormat is what the device's WAVEFORMATEX(TENSIBLE) says.
type mixFormat struct {
	rate, channels, bits int
	float                bool
}

func parseMixFormat(b []byte) (mixFormat, error) {
	tag := binary.LittleEndian.Uint16(b)
	f := mixFormat{
		channels: int(binary.LittleEndian.Uint16(b[2:])),
		rate:     int(binary.LittleEndian.Uint32(b[4:])),
		bits:     int(binary.LittleEndian.Uint16(b[14:])),
	}
	if tag == waveFormatExtensible && len(b) >= waveFormatExtensibleLen {
		tag = binary.LittleEndian.Uint16(b[24:]) // SubFormat's first field is the format tag
	}
	switch {
	case tag == waveFormatIEEEFloat && f.bits == 32:
		f.float = true
	case tag == waveFormatPCM && (f.bits == 16 || f.bits == 32):
	default:
		return f, fmt.Errorf("the sound device's format is not supported (tag %#x, %d bits)", tag, f.bits)
	}
	if f.channels < 1 || f.rate < 8000 {
		return f, errors.New("the sound device reports no usable format")
	}
	return f, nil
}

func openAudio(OpenOptions) (AudioSource, error) {
	a := &wasapiAudio{ch: make(chan []int16, audioQueued), done: make(chan struct{})}
	ready := make(chan error, 1)
	go a.run(ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return a, nil
}

const audioQueued = 50 // reads waiting for Read, about a second of sound

func (a *wasapiAudio) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(a.done)
	started := false
	err := a.capture(func() { started = true; ready <- nil })
	if !started {
		ready <- err
		return
	}
	a.err = err
	if a.err == nil {
		a.err = errors.New("sound capture closed")
	}
	close(a.ch)
}

// capture records until Close. It calls started once recording runs.
func (a *wasapiAudio) capture(started func()) error {
	// A fresh locked thread: COM is not initialized here yet (S_FALSE, 1,
	// would mean it was, which also needs the matching uninitialize).
	if err := windows.CoInitializeEx(0, coinitMultithreaded); err != nil && err != syscall.Errno(1) {
		return fmt.Errorf("capture sound: CoInitializeEx: %w", err)
	}
	defer windows.CoUninitialize()

	var enum, device, client, capture uintptr
	defer func() {
		comRelease(capture)
		comRelease(client)
		comRelease(device)
		comRelease(enum)
	}()
	r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidMMDeviceEnumerator)), 0, clsctxAll,
		uintptr(unsafe.Pointer(&iidIMMDeviceEnumerator)), uintptr(unsafe.Pointer(&enum)))
	if err := hresult("capture sound: create the device enumerator", r); err != nil {
		return err
	}
	if err := hresult("capture sound: no sound output device", comCall(enum, vtEnumGetDefault, eRender, eConsole, uintptr(unsafe.Pointer(&device)))); err != nil {
		return err
	}
	if err := hresult("capture sound: open the sound device", comCall(device, vtDeviceActivate,
		uintptr(unsafe.Pointer(&iidIAudioClient)), clsctxAll, 0, uintptr(unsafe.Pointer(&client)))); err != nil {
		return err
	}
	var wfx uintptr
	if err := hresult("capture sound: read the device format", comCall(client, vtClientGetMixFormat, uintptr(unsafe.Pointer(&wfx)))); err != nil {
		return err
	}
	defer windows.CoTaskMemFree(ptr(wfx))
	n := waveFormatExSize + int(*(*uint16)(ptr(wfx + 16))) // + cbSize
	f, err := parseMixFormat(unsafe.Slice((*byte)(ptr(wfx)), max(n, waveFormatExSize)))
	if err != nil {
		return fmt.Errorf("capture sound: %w", err)
	}
	if err := hresult("capture sound: start loopback capture", comCall(client, vtClientInitialize, audclntShareModeShared,
		audclntStreamLoopback, uintptr(wasapiBuffer/100), 0, wfx, 0)); err != nil {
		return err
	}
	if err := hresult("capture sound: open the capture client", comCall(client, vtClientGetService,
		uintptr(unsafe.Pointer(&iidIAudioCaptureClient)), uintptr(unsafe.Pointer(&capture)))); err != nil {
		return err
	}
	if err := hresult("capture sound: start", comCall(client, vtClientStart)); err != nil {
		return err
	}
	defer comCall(client, vtClientStop)

	factor := 1
	for f.rate/factor > maxWireRate {
		factor++
	}
	a.rate, a.channels = f.rate/factor, min(f.channels, 2)
	started()
	conv := converter{f: f, factor: factor}
	for !a.stop.Load() {
		time.Sleep(wasapiPoll)
		for {
			var frames uint32
			if err := hresult("capture sound", comCall(capture, vtCaptureGetNextPacket, uintptr(unsafe.Pointer(&frames)))); err != nil {
				return err
			}
			if frames == 0 {
				break
			}
			var data uintptr
			var flags uint32
			if err := hresult("capture sound", comCall(capture, vtCaptureGetBuffer, uintptr(unsafe.Pointer(&data)),
				uintptr(unsafe.Pointer(&frames)), uintptr(unsafe.Pointer(&flags)), 0, 0)); err != nil {
				return err
			}
			var raw []byte
			if flags&audclntBufferSilent == 0 && data != 0 {
				raw = unsafe.Slice((*byte)(ptr(data)), int(frames)*f.channels*f.bits/8)
			}
			s := conv.convert(raw, int(frames))
			comCall(capture, vtCaptureReleaseBuffer, uintptr(frames))
			select {
			case a.ch <- s:
			default: // Read is not keeping up: drop rather than lag
			}
		}
	}
	return nil
}

// converter turns the device's samples into 16-bit mono or stereo at
// rate/factor. Decimation averages factor frames, which is enough of a
// low-pass filter for what remote desktop sound is used for.
type converter struct {
	f      mixFormat
	factor int
	acc    [2]float64
	n      int
}

func (c *converter) sample(raw []byte, i int) float64 {
	if c.f.float {
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:])))
	}
	if c.f.bits == 16 {
		return float64(int16(binary.LittleEndian.Uint16(raw[2*i:]))) / 32768
	}
	return float64(int32(binary.LittleEndian.Uint32(raw[4*i:]))) / 2147483648
}

// convert converts frames frames of raw (nil = silence).
func (c *converter) convert(raw []byte, frames int) []int16 {
	ch := min(c.f.channels, 2)
	out := make([]int16, 0, (frames/c.factor+1)*ch)
	for i := 0; i < frames; i++ {
		for k := 0; k < ch; k++ {
			if raw != nil {
				c.acc[k] += c.sample(raw, i*c.f.channels+k)
			}
		}
		c.n++
		if c.n == c.factor {
			for k := 0; k < ch; k++ {
				out = append(out, floatSample(float32(c.acc[k]/float64(c.factor))))
				c.acc[k] = 0
			}
			c.n = 0
		}
	}
	return out
}

func (a *wasapiAudio) Format() (int, int) { return a.rate, a.channels }

func (a *wasapiAudio) Read() ([]int16, error) {
	s, ok := <-a.ch
	if !ok {
		return nil, a.err
	}
	return s, nil
}

func (a *wasapiAudio) Close() error {
	a.once.Do(func() {
		a.stop.Store(true)
		<-a.done
	})
	return nil
}
