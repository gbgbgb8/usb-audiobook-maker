package device

import (
	"context"
	"fmt"
	"testing"
)

type fakeRunner struct {
	outputs map[string]string
}

func (f fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	key := name
	for _, arg := range args {
		key += " " + arg
	}
	output, ok := f.outputs[key]
	if !ok {
		return nil, fmt.Errorf("unexpected command: %s", key)
	}
	return []byte(output), nil
}

func TestDiscoverAllowsOnlyExternalPhysicalUSBWholeDisks(t *testing.T) {
	runner := fakeRunner{outputs: map[string]string{
		"diskutil list -plist external physical": plist(mapBody(
			keyArray("AllDisks", stringValueXML("disk11"), stringValueXML("disk11s1"), stringValueXML("disk12")),
			keyArray("AllDisksAndPartitions",
				dictValue(
					keyString("DeviceIdentifier", "disk11"),
					keyString("Content", "FDisk_partition_scheme"),
					keyArray("Partitions", dictValue(
						keyString("DeviceIdentifier", "disk11s1"),
						keyString("VolumeName", "NO NAME"),
						keyString("MountPoint", "/Volumes/NO NAME"),
						keyString("Content", "DOS_FAT_32"),
					)),
				),
				dictValue(keyString("DeviceIdentifier", "disk12")),
			),
		)),
		"diskutil info -plist disk11": plist(mapBody(
			keyString("DeviceIdentifier", "disk11"),
			keyString("BusProtocol", "USB"),
			keyInteger("TotalSize", 4026531840),
			keyFalse("Internal"),
		)),
		"diskutil info -plist disk12": plist(mapBody(
			keyString("DeviceIdentifier", "disk12"),
			keyString("BusProtocol", "Thunderbolt"),
			keyInteger("TotalSize", 999),
			keyFalse("Internal"),
		)),
	}}

	devices, err := Discover(context.Background(), runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 {
		t.Fatalf("got %d devices, want 1: %#v", len(devices), devices)
	}
	got := devices[0]
	if got.Identifier != "disk11" || got.VolumeID != "disk11s1" ||
		got.MountPoint != "/Volumes/NO NAME" || got.Filesystem != "DOS_FAT_32" ||
		got.Scheme != "FDisk_partition_scheme" {
		t.Fatalf("unexpected device: %#v", got)
	}
}

func TestSafeTarget(t *testing.T) {
	tests := []struct {
		name   string
		device Device
		want   bool
	}{
		{"USB", Device{Identifier: "disk11", Protocol: "USB"}, true},
		{"partition", Device{Identifier: "disk11s1", Protocol: "USB"}, false},
		{"internal", Device{Identifier: "disk1", Protocol: "USB", Internal: true}, false},
		{"disk image", Device{Identifier: "disk9", Protocol: "Disk Image"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.device.SafeTarget(); got != test.want {
				t.Fatalf("SafeTarget() = %v, want %v", got, test.want)
			}
		})
	}
}

func plist(body string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<plist version="1.0"><dict>` + body + `</dict></plist>`
}

func mapBody(values ...string) string {
	result := ""
	for _, value := range values {
		result += value
	}
	return result
}

func keyString(key, value string) string {
	return "<key>" + key + "</key><string>" + value + "</string>"
}

func keyInteger(key string, value int64) string {
	return fmt.Sprintf("<key>%s</key><integer>%d</integer>", key, value)
}

func keyFalse(key string) string {
	return "<key>" + key + "</key><false/>"
}

func stringValueXML(value string) string {
	return "<string>" + value + "</string>"
}

func dictValue(values ...string) string {
	return "<dict>" + mapBody(values...) + "</dict>"
}

func keyArray(key string, values ...string) string {
	return "<key>" + key + "</key><array>" + mapBody(values...) + "</array>"
}
