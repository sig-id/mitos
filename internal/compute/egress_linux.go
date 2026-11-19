//go:build linux

package compute

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// loopbackUp raises the namespace's loopback interface through netlink. The
// sandbox owns its user and network namespaces, so no privileges are needed
// beyond the ones the namespace already grants. Loopback is always interface
// index 1 on Linux.
func loopbackUp() error {
	socket, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer unix.Close(socket)
	// One RTM_NEWLINK message: nlmsghdr (16 bytes) + ifinfomsg (16 bytes).
	var message bytes.Buffer
	binary.Write(&message, binary.LittleEndian, uint32(unix.NLMSG_HDRLEN+unix.SizeofIfInfomsg))
	binary.Write(&message, binary.LittleEndian, uint16(unix.RTM_NEWLINK))
	binary.Write(&message, binary.LittleEndian, uint16(unix.NLM_F_REQUEST|unix.NLM_F_ACK))
	binary.Write(&message, binary.LittleEndian, uint32(0)) // sequence
	binary.Write(&message, binary.LittleEndian, uint32(0)) // pid
	binary.Write(&message, binary.LittleEndian, uint8(unix.AF_UNSPEC))
	binary.Write(&message, binary.LittleEndian, uint8(0))  // pad
	binary.Write(&message, binary.LittleEndian, uint16(0)) // type
	binary.Write(&message, binary.LittleEndian, int32(1))  // loopback index
	binary.Write(&message, binary.LittleEndian, uint32(unix.IFF_UP))
	binary.Write(&message, binary.LittleEndian, uint32(unix.IFF_UP))
	if err = unix.Sendmsg(socket, message.Bytes(), nil, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}, 0); err != nil {
		return err
	}
	reply := make([]byte, 4096)
	n, _, err := unix.Recvfrom(socket, reply, 0)
	if err != nil {
		return err
	}
	if n < unix.NLMSG_HDRLEN+4 {
		return errors.New("netlink acknowledgement missing")
	}
	if kind := binary.LittleEndian.Uint16(reply[4:6]); kind != unix.NLMSG_ERROR {
		return errors.New("netlink acknowledgement missing")
	}
	if errno := int32(binary.LittleEndian.Uint32(reply[unix.NLMSG_HDRLEN:])); errno != 0 {
		return fmt.Errorf("netlink refused loopback up: errno %d", errno)
	}
	return nil
}
