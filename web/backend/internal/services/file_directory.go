package services

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

type ManagedFiles interface {
	RemoteFiles
	Page(string, int, int) (RemoteFileList, error)
	Metadata(string) (RemoteFileEntry, error)
	Mkdir(string, bool) error
	Rename(string, string) error
	Remove(string) error
}

func FileMetadataVersion(entry RemoteFileEntry) string {
	return FileRevision([]byte(fmt.Sprintf("%s:%d:%d:%s:%t", entry.Path, entry.Size, entry.ModifiedAt.UnixNano(), entry.Mode, entry.IsSymlink)))
}
func fileEntry(filename string, info os.FileInfo) RemoteFileEntry {
	return RemoteFileEntry{Name: path.Base(filename), Path: filename, Size: info.Size(), ModifiedAt: info.ModTime().UTC(), Mode: info.Mode().String(), IsDir: info.IsDir(), IsFile: info.Mode().IsRegular(), IsSymlink: info.Mode()&os.ModeSymlink != 0}
}
func pageResult(directory string, entries []RemoteFileEntry, offset, limit int) RemoteFileList {
	result := RemoteFileList{Path: directory, Parent: remoteFileParent(directory), Entries: entries}
	if len(entries) > limit {
		result.Entries = entries[:limit]
		result.NextCursor = strconv.Itoa(offset + limit)
	}
	if result.Entries == nil {
		result.Entries = []RemoteFileEntry{}
	}
	SortRemoteFiles(&result)
	return result
}

type directoryProtocol struct {
	reader   io.Reader
	writer   io.Writer
	sequence uint32
}

func protocolString(value string) []byte {
	result := make([]byte, 4+len(value))
	binary.BigEndian.PutUint32(result, uint32(len(value)))
	copy(result[4:], value)
	return result
}
func (protocol *directoryProtocol) send(kind byte, payload []byte) (byte, []byte, error) {
	protocol.sequence++
	packet := make([]byte, 9+len(payload))
	binary.BigEndian.PutUint32(packet, uint32(len(packet)-4))
	packet[4] = kind
	binary.BigEndian.PutUint32(packet[5:], protocol.sequence)
	copy(packet[9:], payload)
	if kind == 1 {
		packet = packet[:9]
		binary.BigEndian.PutUint32(packet, 5)
		binary.BigEndian.PutUint32(packet[5:], 3)
	}
	if _, err := protocol.writer.Write(packet); err != nil {
		return 0, nil, err
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(protocol.reader, header); err != nil {
		return 0, nil, err
	}
	size := binary.BigEndian.Uint32(header)
	if size < 5 || size > 1<<20 {
		return 0, nil, errors.New("invalid SFTP directory packet")
	}
	response := make([]byte, size)
	if _, err := io.ReadFull(protocol.reader, response); err != nil {
		return 0, nil, err
	}
	if kind == 1 && binary.BigEndian.Uint32(response[1:5]) != 3 {
		return 0, nil, errors.New("SFTP v3 required")
	}
	if kind != 1 && binary.BigEndian.Uint32(response[1:5]) != protocol.sequence {
		return 0, nil, errors.New("unexpected SFTP request id")
	}
	return response[0], response[5:], nil
}

type attributeReader struct {
	*bytes.Reader
	err error
}

func (reader *attributeReader) number() uint32 {
	var value uint32
	if reader.err == nil {
		reader.err = binary.Read(reader.Reader, binary.BigEndian, &value)
	}
	return value
}
func (reader *attributeReader) text() string {
	size := reader.number()
	if reader.err != nil {
		return ""
	}
	if size > uint32(reader.Len()) {
		reader.err = io.ErrUnexpectedEOF
		return ""
	}
	value := make([]byte, size)
	_, reader.err = io.ReadFull(reader.Reader, value)
	return string(value)
}
func (reader *attributeReader) entry(directory string) RemoteFileEntry {
	name := reader.text()
	reader.text()
	flags := reader.number()
	var size uint64
	if flags&1 != 0 && reader.err == nil {
		reader.err = binary.Read(reader.Reader, binary.BigEndian, &size)
	}
	if flags&2 != 0 {
		reader.number()
		reader.number()
	}
	mode := uint32(0)
	if flags&4 != 0 {
		mode = reader.number()
	}
	modified := uint32(0)
	if flags&8 != 0 {
		reader.number()
		modified = reader.number()
	}
	if flags&0x80000000 != 0 {
		count := reader.number()
		if count > 1024 {
			reader.err = errors.New("too many SFTP attributes")
		} else {
			for index := uint32(0); index < count; index++ {
				reader.text()
				reader.text()
			}
		}
	}
	permissions := os.FileMode(mode & 0777)
	if mode&0170000 == 0040000 {
		permissions |= os.ModeDir
	}
	if mode&0170000 == 0120000 {
		permissions |= os.ModeSymlink
	}
	return RemoteFileEntry{Name: name, Path: path.Join(directory, name), Size: int64(size), ModifiedAt: time.Unix(int64(modified), 0).UTC(), Mode: permissions.String(), IsDir: mode&0170000 == 0040000, IsFile: mode&0170000 == 0100000, IsSymlink: mode&0170000 == 0120000}
}
func directoryStatus(payload []byte) error {
	if len(payload) < 4 {
		return io.ErrUnexpectedEOF
	}
	switch binary.BigEndian.Uint32(payload) {
	case 0:
		return nil
	case 1:
		return io.EOF
	case 2:
		return os.ErrNotExist
	case 3:
		return os.ErrPermission
	default:
		return errors.New("SFTP directory request failed")
	}
}
func readDirectoryPage(reader io.Reader, writer io.Writer, directory string, offset, limit int) (RemoteFileList, error) {
	protocol := directoryProtocol{reader: reader, writer: writer}
	kind, _, err := protocol.send(1, nil)
	if err != nil {
		return RemoteFileList{}, err
	}
	if kind != 2 {
		return RemoteFileList{}, errors.New("SFTP v3 required")
	}
	kind, payload, err := protocol.send(16, protocolString(directory))
	if err != nil {
		return RemoteFileList{}, err
	}
	if kind == 101 {
		return RemoteFileList{}, directoryStatus(payload)
	}
	if kind != 104 {
		return RemoteFileList{}, errors.New("invalid realpath response")
	}
	attrs := attributeReader{Reader: bytes.NewReader(payload)}
	if attrs.number() != 1 {
		return RemoteFileList{}, ErrFilePath
	}
	canonical := attrs.text()
	if _, err = CleanRemotePath(canonical, false); err != nil {
		return RemoteFileList{}, err
	}
	kind, payload, err = protocol.send(11, protocolString(canonical))
	if err != nil {
		return RemoteFileList{}, err
	}
	if kind == 101 {
		return RemoteFileList{}, directoryStatus(payload)
	}
	if kind != 102 {
		return RemoteFileList{}, errors.New("invalid directory handle")
	}
	handleReader := attributeReader{Reader: bytes.NewReader(payload)}
	handle := handleReader.text()
	if handleReader.err != nil {
		return RemoteFileList{}, handleReader.err
	}
	defer protocol.send(4, protocolString(handle))
	entries := []RemoteFileEntry{}
	seen := 0
	for len(entries) <= limit {
		kind, payload, err = protocol.send(12, protocolString(handle))
		if err != nil {
			return RemoteFileList{}, err
		}
		if kind == 101 {
			err = directoryStatus(payload)
			if errors.Is(err, io.EOF) {
				break
			}
			if err == nil {
				err = errors.New("unexpected directory status")
			}
			return RemoteFileList{}, err
		}
		if kind != 104 {
			return RemoteFileList{}, errors.New("invalid directory entries")
		}
		attrs = attributeReader{Reader: bytes.NewReader(payload)}
		count := attrs.number()
		if count > 10000 {
			return RemoteFileList{}, errors.New("directory batch too large")
		}
		for index := uint32(0); index < count; index++ {
			entry := attrs.entry(canonical)
			if attrs.err != nil {
				return RemoteFileList{}, attrs.err
			}
			if entry.Name == "." || entry.Name == ".." {
				continue
			}
			if strings.Contains(entry.Name, "/") {
				return RemoteFileList{}, ErrFilePath
			}
			if seen >= offset {
				entries = append(entries, entry)
			}
			seen++
			if len(entries) > limit {
				break
			}
		}
	}
	return pageResult(canonical, entries, offset, limit), nil
}
func (store *sftpFiles) Page(directory string, offset, limit int) (RemoteFileList, error) {
	if store.transport == nil {
		return RemoteFileList{}, errors.New("SSH transport required")
	}
	session, err := store.transport.NewSession()
	if err != nil {
		return RemoteFileList{}, err
	}
	defer session.Close()
	stop := context.AfterFunc(store.context, func() { _ = session.Close() })
	defer stop()
	input, err := session.StdinPipe()
	if err != nil {
		return RemoteFileList{}, err
	}
	output, err := session.StdoutPipe()
	if err != nil {
		return RemoteFileList{}, err
	}
	if err = session.RequestSubsystem("sftp"); err != nil {
		return RemoteFileList{}, err
	}
	result, err := readDirectoryPage(output, input, directory, offset, limit)
	if err == nil {
		for index, entry := range result.Entries {
			if entry.IsSymlink {
				if info, statErr := store.client.Stat(entry.Path); statErr == nil {
					result.Entries[index] = fileEntry(entry.Path, info)
					result.Entries[index].IsSymlink = true
				}
			}
		}
	}
	return result, err
}
