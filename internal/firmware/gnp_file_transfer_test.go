package firmware

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type cameraFilePeer struct {
	hash                 hash.Hash
	size, wanted         int64
	block, starts, syncs int
	cancels              int
	listed               bool
	corrupt              bool
	failBlock            int
	existing             *gnpCameraFile
}

func newCameraFilePeer() *cameraFilePeer { return &cameraFilePeer{hash: md5.New(), failBlock: -1} }
func (p *cameraFilePeer) Request(ctx context.Context, flags byte, body []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(body) < 2 {
		return nil, errors.New("missing file command")
	}
	if body[0] == 3 {
		if flags != 0x40 || len(body) != 2 {
			return nil, errors.New("invalid file directory request")
		}
		entry := p.existing
		if p.starts > 0 {
			digest := p.hash.Sum(nil)
			if p.corrupt {
				digest[0] ^= 1
			}
			entry = &gnpCameraFile{Name: "upgrade.zip", Size: uint32(p.size)}
			copy(entry.MD5[:], digest)
		}
		if body[1] == 0 {
			p.listed = true
			count := byte(0)
			if entry != nil {
				count = 1
			}
			return []byte{1, count}, nil
		}
		if body[1] != 0x40 || !p.listed || entry == nil {
			return nil, errors.New("directory cursor out of order")
		}
		p.listed = false
		data := make([]byte, 25+len(entry.Name))
		data[0] = 0x40 | byte(len(data)-1)
		copy(data[5:21], entry.MD5[:])
		binary.BigEndian.PutUint32(data[21:25], entry.Size)
		copy(data[25:], entry.Name)
		return data, nil
	}
	if body[0] != 0 {
		return nil, errors.New("wrong camera file operation")
	}
	if len(body) == 2 && body[1] == 0x80 && flags == 0x80 {
		p.cancels++
		return nil, nil
	}
	if body[1]&0xc0 == 0 {
		if p.starts != 0 || flags != 0x80 || len(body) != 17 || body[1] != 15 || string(body[6:]) != "upgrade.zip" {
			return nil, errors.New("wrong camera file start")
		}
		p.starts++
		p.wanted = int64(binary.BigEndian.Uint32(body[2:6]))
		return nil, nil
	}
	if p.starts != 1 || len(body) < 4 || len(body) > 58 || body[1]&0xc0 != 0x40 || int(body[1]&0x3f) != len(body)-2 || body[2] != byte(p.block) {
		return nil, errors.New("wrong file chunk or wrapped sequence")
	}
	if (p.block%100 == 0 && flags != 0x80) || (p.block%100 != 0 && flags != 0) {
		return nil, errors.New("wrong file synchronization cadence")
	}
	if p.block == p.failBlock {
		return nil, errors.New("injected file write failure")
	}
	if flags == 0x80 {
		p.syncs++
	}
	p.block++
	p.size += int64(len(body) - 3)
	_, _ = p.hash.Write(body[3:])
	return nil, nil
}

func TestCameraGNPFileTransfer(t *testing.T) {
	for _, size := range []int{1, 54, 55, 56, 5500, 14081} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			data := make([]byte, size)
			for i := range data {
				data[i] = byte(i*29 + 11)
			}
			peer := newCameraFilePeer()
			digest := md5.Sum(data)
			if err := transferCameraGNPFile(context.Background(), peer, bytes.NewReader(data), int64(size), digest, nil); err != nil {
				t.Fatal(err)
			}
			if peer.size != int64(size) || !bytes.Equal(peer.hash.Sum(nil), digest[:]) || peer.block != (size+54)/55 || peer.syncs != (peer.block+99)/100 {
				t.Fatal("incomplete file transfer")
			}
		})
	}
}
func TestCameraGNPFileSkipAndFailures(t *testing.T) {
	data := bytes.Repeat([]byte{3, 9, 1}, 100)
	digest := md5.Sum(data)
	for _, mode := range []string{"matching", "wrong-md5", "write-failure", "short-source", "long-source", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			peer := newCameraFilePeer()
			source := bytes.NewReader(data)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch mode {
			case "matching":
				peer.existing = &gnpCameraFile{Name: "upgrade.zip", Size: uint32(len(data)), MD5: digest}
			case "wrong-md5":
				peer.corrupt = true
			case "write-failure":
				peer.failBlock = 2
			case "short-source":
				source = bytes.NewReader(data[:100])
			case "long-source":
				source = bytes.NewReader(append(append([]byte(nil), data...), 0))
			case "cancelled":
				cancel()
			}
			err := transferCameraGNPFile(ctx, peer, source, int64(len(data)), digest, nil)
			if mode == "matching" {
				if err != nil || peer.starts != 0 {
					t.Fatal("matching staged file was overwritten", err)
				}
				return
			}
			if err == nil {
				t.Fatal("failed file transfer was accepted")
			}
			if mode == "write-failure" && peer.block != 2 {
				t.Fatal("transfer continued after write failure")
			}
			if mode != "cancelled" && peer.cancels != 1 {
				t.Fatal("incomplete stream was not cancelled", peer.cancels)
			}
		})
	}
}

type cameraFileWire struct {
	peer       *cameraFilePeer
	replies    chan []byte
	fullFrames int
}

func (w *cameraFileWire) Write(ctx context.Context, packet []byte) error {
	if len(packet) != 64 || packet[0] != 5 || packet[1] != 1 || packet[2] != 0 || packet[3] == 0 || packet[5] != 3 {
		return errors.New("invalid GNP camera frame")
	}
	length := int(packet[4] & 63)
	if length < 7 || length > 63 {
		return errors.New("invalid GNP camera payload length")
	}
	if length == 63 {
		w.fullFrames++
	}
	flags := packet[4] & 0xc0
	body := packet[6 : 1+length]
	data, err := w.peer.Request(ctx, flags, body)
	if err != nil {
		return err
	}
	if flags == 0 {
		return nil
	}
	reply := make([]byte, 64)
	reply[0], reply[1], reply[2], reply[3] = 5, 0, 1, packet[3]
	if flags == 0x40 {
		reply[4], reply[5], reply[6] = 0xc0|byte(6+len(data)), 3, body[0]
		copy(reply[7:], data)
	} else {
		reply[4], reply[5] = 0xca, 0xff
		copy(reply[6:], packet[1:6])
	}
	w.replies <- reply
	return nil
}
func (w *cameraFileWire) Read(ctx context.Context) ([]byte, error) {
	select {
	case reply := <-w.replies:
		return reply, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type cameraFileWireRequests struct{ runtime *sitelRuntime }

func (c cameraFileWireRequests) Request(ctx context.Context, flags byte, body []byte) ([]byte, error) {
	return c.runtime.exchange(ctx, 1, 3, flags, body)
}

func TestCameraGNPUsesFullFramesAndParameterizedReads(t *testing.T) {
	data := bytes.Repeat([]byte{7, 4, 1}, 6000)
	wire := &cameraFileWire{peer: newCameraFilePeer(), replies: make(chan []byte, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := transferCameraGNPFile(ctx, cameraFileWireRequests{runtime: &sitelRuntime{io: wire}}, bytes.NewReader(data), int64(len(data)), md5.Sum(data), nil); err != nil {
		t.Fatal(err)
	}
	if wire.fullFrames == 0 || wire.peer.size != int64(len(data)) {
		t.Fatal("full 63-byte GNP packets were not sent")
	}
}

func TestPanaCast50OriginalBundle(t *testing.T) {
	path := os.Getenv("JABRIDGE_TEST_PANACAST50_AUDIT")
	if path == "" {
		t.Skip("original bundle is a private opt-in fixture")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Protocol      int
		Path, Version string
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, row := range rows {
		if row.Protocol != 10 || row.Path == "" {
			continue
		}
		count++
		t.Run(filepath.Base(row.Path), func(t *testing.T) {
			archive, err := loadPanaCast50Archive(row.Path)
			if err != nil {
				t.Fatal(err)
			}
			if archive.Manifest.Version != row.Version {
				t.Fatal("wrong bundle version")
			}
			if err := ValidateInstallInput([]string{row.Path}); err != nil {
				t.Fatal("PanaCast 50 preflight dispatch", err)
			}
			if os.Getenv("JABRIDGE_TEST_LIVE_RELEASES") == "1" {
				for _, pid := range panacast50PIDs {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					err := verifyPanaCast50Release(ctx, row.Path, archive, pid)
					cancel()
					if err != nil {
						t.Fatalf("official release %04x: %v", pid, err)
					}
				}
			}
			peer := newCameraFilePeer()
			if err := transferCameraGNPFile(context.Background(), peer, bytes.NewReader(archive.Data), int64(len(archive.Data)), archive.MD5, nil); err != nil {
				t.Fatal(err)
			}
			t.Logf("full inner bundle verified and reconstructed: %d bytes, %d data blocks, %d synchronization ACKs", peer.size, peer.block, peer.syncs)
		})
	}
	if count != 1 {
		t.Fatal("missing original PanaCast 50 fixture")
	}
}
