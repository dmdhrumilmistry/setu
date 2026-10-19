//go:build darwin

package desktop

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// macAudio captures the sound output with ScreenCaptureKit (macOS 13 and
// later), which needs the same Screen Recording permission as the screen.
// The stream's video is kept as small and slow as ScreenCaptureKit allows
// and ignored: screen updates come from CoreGraphics (screen_darwin.go).
type macAudio struct {
	stream objc.ID
	output objc.ID
	queue  uintptr
	ch     chan []int16
	done   chan struct{}
	once   sync.Once
}

const (
	audioRate             = 48000
	scStreamOutputAudio   = 1
	audioFormatFloat      = 1      // kAudioFormatFlagIsFloat
	audioFormatNonInterl  = 1 << 5 // kAudioFormatFlagIsNonInterleaved
	sampleBufferAlign16   = 1      // kCMSampleBufferFlag_AudioBufferList_Assure16ByteAlignment
	audioStartTimeout     = 10 * time.Second
	audioQueuedPackets    = 50 // about a second of sound waiting for Read
	audioMaxBufferChannel = 8
)

// audioStreamDesc is AudioStreamBasicDescription.
type audioStreamDesc struct {
	SampleRate       float64
	FormatID         uint32
	FormatFlags      uint32
	BytesPerPacket   uint32
	FramesPerPacket  uint32
	BytesPerFrame    uint32
	ChannelsPerFrame uint32
	BitsPerChannel   uint32
	Reserved         uint32
}

// audioBufferList is AudioBufferList with room for audioMaxBufferChannel buffers.
type audioBufferList struct {
	N    uint32
	_    uint32
	Bufs [audioMaxBufferChannel]struct {
		Channels uint32
		Size     uint32
		Data     uintptr
	}
}

// cmTime is CMTime.
type cmTime struct {
	Value     int64
	Timescale int32
	Flags     uint32
	Epoch     int64
}

var (
	sckOnce sync.Once
	sckErr  error

	dispatchQueueCreate                     func(label *byte, attr uintptr) uintptr
	dispatchRelease                         func(obj uintptr)
	cmSampleBufferGetFormatDescription      func(sbuf uintptr) uintptr
	cmAudioFormatDescriptionGetStreamBasic  func(desc uintptr) *audioStreamDesc
	cmSampleBufferGetAudioBufferListWithRBB func(sbuf uintptr, sizeOut *uintptr, abl *audioBufferList, ablSize uintptr, alloc, memAlloc uintptr, flags uint32, block *uintptr) int32

	audioOutputs   sync.Map // objc.ID of an output object -> *macAudio
	audioClass     objc.Class
	audioClassOnce sync.Once
	audioClassErr  error
)

var (
	selGetShareable     = objc.RegisterName("getShareableContentWithCompletionHandler:")
	selDisplays         = objc.RegisterName("displays")
	selFirstObject      = objc.RegisterName("firstObject")
	selArray            = objc.RegisterName("array")
	selAlloc            = objc.RegisterName("alloc")
	selInit             = objc.RegisterName("init")
	selRelease          = objc.RegisterName("release")
	selRetain           = objc.RegisterName("retain")
	selInitDisplayExcl  = objc.RegisterName("initWithDisplay:excludingWindows:")
	selSetCapturesAudio = objc.RegisterName("setCapturesAudio:")
	selSetSampleRate    = objc.RegisterName("setSampleRate:")
	selSetChannelCount  = objc.RegisterName("setChannelCount:")
	selSetExcludesSelf  = objc.RegisterName("setExcludesCurrentProcessAudio:")
	selSetWidth         = objc.RegisterName("setWidth:")
	selSetHeight        = objc.RegisterName("setHeight:")
	selSetMinInterval   = objc.RegisterName("setMinimumFrameInterval:")
	selSetQueueDepth    = objc.RegisterName("setQueueDepth:")
	selInitFilterConfig = objc.RegisterName("initWithFilter:configuration:delegate:")
	selAddOutput        = objc.RegisterName("addStreamOutput:type:sampleHandlerQueue:error:")
	selStartCapture     = objc.RegisterName("startCaptureWithCompletionHandler:")
	selStopCapture      = objc.RegisterName("stopCaptureWithCompletionHandler:")
	selDidOutput        = objc.RegisterName("stream:didOutputSampleBuffer:ofType:")
	selResponds         = objc.RegisterName("instancesRespondToSelector:")
	selLocalizedDesc    = objc.RegisterName("localizedDescription")
)

func loadSCK() error {
	sckOnce.Do(func() {
		if err := load(); err != nil {
			sckErr = err
			return
		}
		lib := func(path string) uintptr {
			h, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
			if err != nil && sckErr == nil {
				sckErr = fmt.Errorf("load %s: %w", path, err)
			}
			return h
		}
		lib("/System/Library/Frameworks/ScreenCaptureKit.framework/ScreenCaptureKit")
		cm := lib("/System/Library/Frameworks/CoreMedia.framework/CoreMedia")
		sys := lib("/usr/lib/libSystem.B.dylib")
		if sckErr != nil {
			sckErr = fmt.Errorf("sharing sound needs macOS 13 or later (%w)", sckErr)
			return
		}
		cfg := objc.GetClass("SCStreamConfiguration")
		if cfg == 0 || !objc.Send[bool](objc.ID(cfg), selResponds, selSetCapturesAudio) {
			sckErr = errors.New("sharing sound needs macOS 13 or later")
			return
		}
		purego.RegisterLibFunc(&dispatchQueueCreate, sys, "dispatch_queue_create")
		purego.RegisterLibFunc(&dispatchRelease, sys, "dispatch_release")
		purego.RegisterLibFunc(&cmSampleBufferGetFormatDescription, cm, "CMSampleBufferGetFormatDescription")
		purego.RegisterLibFunc(&cmAudioFormatDescriptionGetStreamBasic, cm, "CMAudioFormatDescriptionGetStreamBasicDescription")
		purego.RegisterLibFunc(&cmSampleBufferGetAudioBufferListWithRBB, cm, "CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer")
	})
	return sckErr
}

// outputClass is the SCStreamOutput that receives sample buffers.
func outputClass() (objc.Class, error) {
	audioClassOnce.Do(func() {
		var protos []*objc.Protocol
		if p := objc.GetProtocol("SCStreamOutput"); p != nil {
			protos = append(protos, p)
		}
		audioClass, audioClassErr = objc.RegisterClass("SetuAudioOutput", objc.GetClass("NSObject"), protos, nil, []objc.MethodDef{{
			Cmd: selDidOutput,
			Fn: func(self objc.ID, _ objc.SEL, _ objc.ID, sbuf uintptr, typ int) {
				if typ != scStreamOutputAudio {
					return
				}
				if a, ok := audioOutputs.Load(self); ok {
					a.(*macAudio).receive(sbuf)
				}
			},
		}})
	})
	return audioClass, audioClassErr
}

func nsErrorText(e objc.ID) string {
	s := objc.ID(0)
	if e != 0 {
		s = e.Send(selLocalizedDesc)
	}
	if s == 0 {
		return "unknown error"
	}
	n := objc.Send[uintptr](s, selLengthOfBytesUsing, uintptr(nsUTF8StringEncoding))
	p := objc.Send[uintptr](s, selUTF8String)
	if p == 0 || n == 0 {
		return "unknown error"
	}
	return string(unsafe.Slice((*byte)(ptr(p)), n))
}

func openAudio(OpenOptions) (AudioSource, error) {
	if err := loadSCK(); err != nil {
		return nil, err
	}
	if cgPreflightScreenCaptureAccess != nil && !cgPreflightScreenCaptureAccess() {
		return nil, errScreenPermission
	}
	cls, err := outputClass()
	if err != nil {
		return nil, err
	}

	// The filter needs a display; any display's filter captures all sound.
	type shareable struct {
		display objc.ID
		err     string
	}
	got := make(chan shareable, 1)
	blk := objc.NewBlock(func(_ objc.Block, content, nsErr objc.ID) {
		if content == 0 {
			got <- shareable{err: nsErrorText(nsErr)}
			return
		}
		d := content.Send(selDisplays).Send(selFirstObject)
		if d == 0 {
			got <- shareable{err: "no display found"}
			return
		}
		got <- shareable{display: d.Send(selRetain)}
	})
	defer blk.Release()
	objc.ID(objc.GetClass("SCShareableContent")).Send(selGetShareable, blk)
	var sh shareable
	select {
	case sh = <-got:
	case <-time.After(audioStartTimeout):
		return nil, errors.New("macOS did not answer the request to capture sound")
	}
	if sh.display == 0 {
		return nil, fmt.Errorf("capture sound: %s", sh.err)
	}
	defer sh.display.Send(selRelease)

	filter := objc.ID(objc.GetClass("SCContentFilter")).Send(selAlloc).
		Send(selInitDisplayExcl, sh.display, objc.ID(objc.GetClass("NSArray")).Send(selArray))
	defer filter.Send(selRelease)
	cfg := objc.ID(objc.GetClass("SCStreamConfiguration")).Send(selAlloc).Send(selInit)
	defer cfg.Send(selRelease)
	cfg.Send(selSetCapturesAudio, true)
	cfg.Send(selSetSampleRate, audioRate)
	cfg.Send(selSetChannelCount, 2)
	cfg.Send(selSetExcludesSelf, true)
	cfg.Send(selSetWidth, uintptr(2))
	cfg.Send(selSetHeight, uintptr(2))
	cfg.Send(selSetMinInterval, cmTime{Value: 1, Timescale: 1, Flags: 1}) // one frame a second
	cfg.Send(selSetQueueDepth, 3)

	stream := objc.ID(objc.GetClass("SCStream")).Send(selAlloc).Send(selInitFilterConfig, filter, cfg, objc.ID(0))
	if stream == 0 {
		return nil, errors.New("capture sound: ScreenCaptureKit refused the stream")
	}
	label := []byte("setu.audio\x00")
	a := &macAudio{
		stream: stream,
		output: objc.ID(cls).Send(selAlloc).Send(selInit),
		queue:  dispatchQueueCreate(&label[0], 0),
		ch:     make(chan []int16, audioQueuedPackets),
		done:   make(chan struct{}),
	}
	audioOutputs.Store(a.output, a)
	var nsErr objc.ID
	if !objc.Send[bool](stream, selAddOutput, a.output, scStreamOutputAudio, a.queue, &nsErr) {
		a.release()
		return nil, fmt.Errorf("capture sound: %s", nsErrorText(nsErr))
	}

	started := make(chan string, 1)
	startBlk := objc.NewBlock(func(_ objc.Block, nsErr objc.ID) {
		if nsErr != 0 {
			started <- nsErrorText(nsErr)
			return
		}
		started <- ""
	})
	defer startBlk.Release()
	stream.Send(selStartCapture, startBlk)
	select {
	case msg := <-started:
		if msg != "" {
			a.release()
			return nil, fmt.Errorf("capture sound: %s", msg)
		}
	case <-time.After(audioStartTimeout):
		a.Close()
		return nil, errors.New("capture sound: macOS did not start the capture")
	}
	return a, nil
}

func (a *macAudio) Format() (int, int) { return audioRate, 2 }

// receive runs on the capture queue for each sample buffer.
func (a *macAudio) receive(sbuf uintptr) {
	desc := cmAudioFormatDescriptionGetStreamBasic(cmSampleBufferGetFormatDescription(sbuf))
	if desc == nil || desc.FormatFlags&audioFormatFloat == 0 || desc.BitsPerChannel != 32 {
		return
	}
	// CoreMedia wants exactly the list size it needs, not just enough room.
	var need uintptr
	if cmSampleBufferGetAudioBufferListWithRBB(sbuf, &need, nil, 0, 0, 0, sampleBufferAlign16, nil) != 0 {
		return
	}
	var abl audioBufferList
	var block uintptr
	if need > unsafe.Sizeof(abl) || cmSampleBufferGetAudioBufferListWithRBB(sbuf, nil, &abl, need, 0, 0, sampleBufferAlign16, &block) != 0 {
		return
	}
	defer cfRelease(block)
	var out []int16
	switch {
	case desc.FormatFlags&audioFormatNonInterl != 0 && abl.N >= 1:
		// One buffer per channel: interleave the first two.
		l := floats(abl.Bufs[0].Data, abl.Bufs[0].Size)
		r := l
		if abl.N >= 2 {
			r = floats(abl.Bufs[1].Data, abl.Bufs[1].Size)
		}
		n := min(len(l), len(r))
		out = make([]int16, 0, 2*n)
		for i := 0; i < n; i++ {
			out = append(out, floatSample(l[i]), floatSample(r[i]))
		}
	case abl.N == 1 && desc.ChannelsPerFrame >= 1:
		f := floats(abl.Bufs[0].Data, abl.Bufs[0].Size)
		ch := int(desc.ChannelsPerFrame)
		out = make([]int16, 0, len(f)/ch*2)
		for i := 0; i+ch <= len(f); i += ch {
			r := f[i]
			if ch > 1 {
				r = f[i+1]
			}
			out = append(out, floatSample(f[i]), floatSample(r))
		}
	default:
		return
	}
	select {
	case a.ch <- out:
	default: // Read is not keeping up: drop rather than lag
	}
}

func floats(p uintptr, size uint32) []float32 {
	if p == 0 || size < 4 {
		return nil
	}
	return unsafe.Slice((*float32)(ptr(p)), size/4)
}

func (a *macAudio) Read() ([]int16, error) {
	select {
	case s := <-a.ch:
		return s, nil
	case <-a.done:
		return nil, errors.New("sound capture closed")
	}
}

func (a *macAudio) Close() error {
	a.once.Do(func() {
		close(a.done)
		stopped := make(chan struct{})
		blk := objc.NewBlock(func(objc.Block, objc.ID) { close(stopped) })
		a.stream.Send(selStopCapture, blk)
		select {
		case <-stopped:
		case <-time.After(audioStartTimeout):
		}
		blk.Release()
		a.release()
	})
	return nil
}

func (a *macAudio) release() {
	audioOutputs.Delete(a.output)
	a.stream.Send(selRelease)
	a.output.Send(selRelease)
	dispatchRelease(a.queue)
}
