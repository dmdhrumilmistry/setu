package rtc

import (
	"reflect"
	"testing"
)

func TestAddresses(t *testing.T) {
	sdp := "v=0\r\n" +
		"a=candidate:1 1 udp 2122260223 192.168.1.20 54400 typ host generation 0\r\n" +
		"a=candidate:2 1 udp 2122260223 4b3c2f0e-7a1d-4f22-9c1e-1f0e2d3c4b5a.local 54401 typ host\r\n" +
		"a=candidate:3 1 udp 1686052607 203.0.113.7 61000 typ srflx raddr 192.168.1.20 rport 54400\r\n" +
		"a=candidate:3 1 udp 1686052607 203.0.113.7 61000 typ srflx raddr 192.168.1.20 rport 54400\r\n" +
		"a=candidate:4 1 udp 41885439 198.51.100.9 3478 typ relay raddr 203.0.113.7 rport 61000\r\n" +
		"a=candidate:5 1 tcp 1518280447 2001:db8::5 9 typ host tcptype active\r\n" +
		"a=candidate:bad\r\n"
	pub, local := Addresses(sdp)
	if !reflect.DeepEqual(pub, []string{"203.0.113.7:61000"}) {
		t.Errorf("public %v", pub)
	}
	if !reflect.DeepEqual(local, []string{"192.168.1.20:54400", "[2001:db8::5]:9"}) {
		t.Errorf("local %v", local)
	}
}
