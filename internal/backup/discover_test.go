package backup

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestCandidateSubnetsReference(t *testing.T) {
	for _, c := range []struct {
		local, override string
		want            []netip.Prefix
	}{{"192.168.1.42", "", []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}}, {"", "", []netip.Prefix{}}, {"172.17.0.2", "10.0.0.0/24, 192.168.5.0/24", []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24"), netip.MustParsePrefix("192.168.5.0/24")}}, {"10.0.0.5", "not-a-cidr, 10.0.0.0/24", []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}}} {
		if got := CandidateSubnets(c.local, c.override); !reflect.DeepEqual(got, c.want) {
			t.Fatal(got, c.want)
		}
	}
}
func TestDiscoveryReference(t *testing.T) {
	d := Discovery{LocalIP: "192.168.1.42", Probe: func(_ context.Context, ip string) bool { return ip == "192.168.1.10" || ip == "192.168.1.99" }, Lookup: func(_ context.Context, ip string) string {
		if ip == "192.168.1.99" {
			return "Synology-DS220"
		}
		return ""
	}}
	got := d.Discover(context.Background(), 32)
	want := []Host{{"192.168.1.99", "Synology-DS220", 445}, {"192.168.1.10", "192.168.1.10", 445}}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	d.Probe = func(context.Context, string) bool { return true }
	if got = d.Discover(context.Background(), 3); len(got) != 3 {
		t.Fatal(got)
	}
	d.LocalIP = ""
	if got = d.Discover(context.Background(), 32); len(got) != 0 {
		t.Fatal(got)
	}
}
func TestListSharesReference(t *testing.T) {
	for _, c := range []struct {
		output string
		want   []string
	}{{"Disk|video|\nDisk|backups|primary backup target\nIPC|IPC$|IPC Service\nDisk|homes|\n", []string{"video", "backups", "homes"}}, {"Sharename Type Comment\n ------ ---- ----\n video Disk\n backups Disk primary backup target\n IPC$ IPC IPC Service\n", []string{"video", "backups"}}} {
		var auth string
		got, e := ListShares(context.Background(), "nas.local", "alice", "pw", "", func(_ context.Context, _ string, args []string) (string, string, error) {
			for i, a := range args {
				if a == "-A" {
					auth = args[i+1]
				}
			}
			info, e := os.Stat(auth)
			if e != nil || info.Mode().Perm() != 0600 {
				t.Fatal(info, e)
			}
			return c.output, "", nil
		})
		if e != nil || !reflect.DeepEqual(got, c.want) {
			t.Fatal(got, e)
		}
		if _, e = os.Stat(auth); !os.IsNotExist(e) {
			t.Fatal("auth file retained")
		}
	}
	_, e := ListShares(context.Background(), "nas.local", "alice", "bad", "", func(context.Context, string, []string) (string, string, error) {
		return "", "session setup failed: NT_STATUS_LOGON_FAILURE\ntree connect failed: NT_STATUS_LOGON_FAILURE", errors.New("exit 1")
	})
	if e == nil || !strings.Contains(e.Error(), "NT_STATUS_LOGON_FAILURE") {
		t.Fatal(e)
	}
	for _, c := range [][2]string{{"", "alice"}, {"nas.local", ""}} {
		if _, e = ListShares(context.Background(), c[0], c[1], "pw", "", nil); e == nil {
			t.Fatal("missing inputs accepted")
		}
	}
}
