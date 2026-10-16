//go:build freebsd || netbsd || openbsd || dragonfly

package desktop

import (
	"errors"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/shm"
	"github.com/jezek/xgb/xproto"
)

// shmBuf is unused on the BSDs, which capture with GetImage.
type shmBuf struct{ data []byte }

func newShm(*xgb.Conn, int) (*shmBuf, error) {
	return nil, errors.New("MIT-SHM is not used on this platform")
}

func (b *shmBuf) getImage(*xgb.Conn, xproto.Window, Display) (*shm.GetImageReply, error) {
	return nil, errors.ErrUnsupported
}

func (b *shmBuf) free(*xgb.Conn) {}
