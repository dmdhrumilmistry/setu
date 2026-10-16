package desktop

import (
	"github.com/jezek/xgb"
	"github.com/jezek/xgb/shm"
	"github.com/jezek/xgb/xproto"
	"golang.org/x/sys/unix"
)

// shmBuf is a MIT-SHM segment the X server writes captures into, which
// avoids copying every frame through the socket.
type shmBuf struct {
	seg  shm.Seg
	data []byte
}

func newShm(c *xgb.Conn, size int) (*shmBuf, error) {
	if err := shm.Init(c); err != nil {
		return nil, err
	}
	id, err := unix.SysvShmGet(unix.IPC_PRIVATE, size, unix.IPC_CREAT|0o600)
	if err != nil {
		return nil, err
	}
	// Marked for removal once both sides detach, so nothing leaks if setu dies.
	defer unix.SysvShmCtl(id, unix.IPC_RMID, nil)
	data, err := unix.SysvShmAttach(id, 0, 0)
	if err != nil {
		return nil, err
	}
	seg, err := shm.NewSegId(c)
	if err == nil {
		err = shm.AttachChecked(c, seg, uint32(id), false).Check()
	}
	if err != nil {
		_ = unix.SysvShmDetach(data)
		return nil, err
	}
	return &shmBuf{seg: seg, data: data}, nil
}

func (b *shmBuf) getImage(c *xgb.Conn, root xproto.Window, d Display) (*shm.GetImageReply, error) {
	return shm.GetImage(c, xproto.Drawable(root), int16(d.X), int16(d.Y), uint16(d.W), uint16(d.H),
		0xffffffff, xproto.ImageFormatZPixmap, b.seg, 0).Reply()
}

func (b *shmBuf) free(c *xgb.Conn) {
	shm.Detach(c, b.seg)
	_ = unix.SysvShmDetach(b.data)
}
