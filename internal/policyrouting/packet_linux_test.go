package policyrouting

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func testIngressPacketMarks(t *testing.T, runner Runner, ctx context.Context) {
	t.Helper()
	guard := `add chain inet foreign_guard before { type filter hook prerouting priority -151; policy accept; }
add rule inet foreign_guard before iifname "xuiup0" meta mark set 0x1234
add chain inet foreign_guard after { type filter hook prerouting priority -149; policy accept; }
add rule inet foreign_guard after iifname "xuiup0" meta mark 0x3e011234 counter comment "preserved-low-bits"
`
	if _, err := runner.Run(ctx, guard, "nft", "-f", "-"); err != nil {
		t.Fatal(err)
	}
	packets := func(table, comment string) uint64 {
		t.Helper()
		data, err := runner.Run(ctx, "", "nft", "-j", "list", "table", "inet", table)
		if err != nil {
			t.Fatal(err)
		}
		var state struct {
			NFTables []struct {
				Rule struct {
					Comment string `json:"comment"`
					Expr    []struct {
						Counter struct {
							Packets uint64 `json:"packets"`
						} `json:"counter"`
					} `json:"expr"`
				} `json:"rule"`
			} `json:"nftables"`
		}
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		for _, entry := range state.NFTables {
			if entry.Rule.Comment == comment {
				for _, expr := range entry.Rule.Expr {
					if expr.Counter.Packets > 0 {
						return expr.Counter.Packets
					}
				}
			}
		}
		return 0
	}
	sendIngressPacket(t, 6, 443)
	sendIngressPacket(t, 6, 444) // Does not match either policy rule.
	sendIngressPacket(t, 17, 53)
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if packets(TableName, "xui-rule:0") == 1 && packets(TableName, "xui-rule:1") == 1 && packets("foreign_guard", "preserved-low-bits") == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("packet matching/mark preservation failed: TUN=%d direct=%d preserved=%d", packets(TableName, "xui-rule:0"), packets(TableName, "xui-rule:1"), packets("foreign_guard", "preserved-low-bits"))
}

func sendIngressPacket(t *testing.T, protocol byte, destinationPort uint16) {
	t.Helper()
	peer, err := net.InterfaceByName("xuiup0p")
	if err != nil {
		t.Fatal(err)
	}
	ingress, err := net.InterfaceByName("xuiup0")
	if err != nil {
		t.Fatal(err)
	}
	headerSize := 20
	if protocol == 17 {
		headerSize = 8
	}
	frame := make([]byte, 14+20+headerSize)
	copy(frame[:6], ingress.HardwareAddr)
	copy(frame[6:12], peer.HardwareAddr)
	binary.BigEndian.PutUint16(frame[12:14], unix.ETH_P_IP)
	ip := frame[14:34]
	ip[0], ip[8], ip[9] = 0x45, 4, protocol
	binary.BigEndian.PutUint16(ip[2:4], uint16(20+headerSize))
	copy(ip[12:16], []byte{192, 0, 2, 3})
	copy(ip[16:20], []byte{203, 0, 113, 7})
	var sum uint32
	for i := 0; i < len(ip); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(ip[i : i+2]))
	}
	for sum > 0xffff {
		sum = sum>>16 + sum&0xffff
	}
	binary.BigEndian.PutUint16(ip[10:12], ^uint16(sum))
	transport := frame[34:]
	binary.BigEndian.PutUint16(transport[:2], 12345)
	binary.BigEndian.PutUint16(transport[2:4], destinationPort)
	if protocol == 6 {
		transport[12], transport[13] = 5<<4, 2
	} else {
		binary.BigEndian.PutUint16(transport[4:6], 8)
	}
	// AF_PACKET expects the EtherType in network byte order.
	ipNetworkOrder := binary.NativeEndian.Uint16(frame[12:14])
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(ipNetworkOrder))
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	address := &unix.SockaddrLinklayer{Ifindex: peer.Index, Protocol: ipNetworkOrder, Halen: 6}
	copy(address.Addr[:], ingress.HardwareAddr)
	if err := unix.Sendto(fd, frame, 0, address); err != nil {
		t.Fatal(err)
	}
}
