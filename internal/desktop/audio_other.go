//go:build !windows && !darwin && !linux && !freebsd && !netbsd && !openbsd && !dragonfly

package desktop

func openAudio(OpenOptions) (AudioSource, error) { return nil, ErrAudioUnsupported }
