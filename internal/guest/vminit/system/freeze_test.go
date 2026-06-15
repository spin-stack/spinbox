//go:build linux

package system

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseFreezableMounts(t *testing.T) {
	// Realistic mountinfo: the rwlayer ext4 is freezable; erofs base layers,
	// overlay rootfs and system mounts are not.
	mountinfo := `21 20 0:19 / /proc rw,nosuid,nodev,noexec - proc procfs rw
22 20 0:20 / /sys rw,nosuid - sysfs sysfsdev rw
23 20 0:5 / /dev rw,nosuid - devtmpfs udev rw
30 20 254:0 / /run/spin-stack/rootfs/0 ro,relatime - erofs /dev/vda ro
31 20 254:16 / /run/spin-stack/rwlayer rw,relatime - ext4 /dev/vdb rw
32 20 0:21 / /run/spin-stack/rootfs ro - overlay overlayfs rw,lowerdir=x,upperdir=y
`
	got := parseFreezableMounts([]byte(mountinfo))
	assert.Equal(t, []string{"/run/spin-stack/rwlayer"}, got)
}

func TestParseFreezableMounts_EscapedPath(t *testing.T) {
	// The kernel octal-escapes spaces in mountinfo path fields.
	mountinfo := "40 20 254:16 / /run/spin\\040data rw - ext4 /dev/vdb rw\n"
	got := parseFreezableMounts([]byte(mountinfo))
	assert.Equal(t, []string{"/run/spin data"}, got)
}

func TestParseFreezableMounts_None(t *testing.T) {
	mountinfo := "30 20 254:0 / /rootfs ro - erofs /dev/vda ro\n"
	assert.Empty(t, parseFreezableMounts([]byte(mountinfo)))
}

func TestUnescapeMountField(t *testing.T) {
	assert.Equal(t, "/plain/path", unescapeMountField("/plain/path"))
	assert.Equal(t, "/a b", unescapeMountField("/a\\040b"))
	assert.Equal(t, "/a\tb", unescapeMountField("/a\\011b"))
	assert.Equal(t, "/a\\b", unescapeMountField("/a\\134b"))
}
