package device

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

type Runner interface {
	Output(context.Context, string, ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

type Device struct {
	Identifier string
	VolumeID   string
	Name       string
	MountPoint string
	Protocol   string
	Filesystem string
	Scheme     string
	SizeBytes  int64
	Internal   bool
}

func (d Device) SafeTarget() bool {
	return d.Identifier != "" &&
		wholeDiskPattern.MatchString(d.Identifier) &&
		!d.Internal &&
		strings.EqualFold(d.Protocol, "USB")
}

var wholeDiskPattern = regexp.MustCompile(`^disk[0-9]+$`)

func Discover(ctx context.Context, runner Runner) ([]Device, error) {
	data, err := runner.Output(ctx, "diskutil", "list", "-plist", "external", "physical")
	if err != nil {
		return nil, fmt.Errorf("list external disks: %w", err)
	}
	root, err := parsePlist(data)
	if err != nil {
		return nil, fmt.Errorf("parse disk list: %w", err)
	}
	all, _ := root["AllDisks"].([]any)
	partitionData := map[string]map[string]any{}
	allWithPartitions, _ := root["AllDisksAndPartitions"].([]any)
	for _, rawDisk := range allWithPartitions {
		disk, ok := rawDisk.(map[string]any)
		if !ok {
			continue
		}
		id := stringValue(disk["DeviceIdentifier"])
		if id != "" {
			partitionData[id] = disk
		}
	}
	var devices []Device
	for _, rawID := range all {
		id, ok := rawID.(string)
		if !ok || !wholeDiskPattern.MatchString(id) {
			continue
		}
		infoData, err := runner.Output(ctx, "diskutil", "info", "-plist", id)
		if err != nil {
			continue
		}
		info, err := parsePlist(infoData)
		if err != nil {
			continue
		}
		device := Device{
			Identifier: stringValue(info["DeviceIdentifier"]),
			Name:       stringValue(info["VolumeName"]),
			MountPoint: stringValue(info["MountPoint"]),
			Protocol:   stringValue(info["BusProtocol"]),
			Filesystem: stringValue(info["FilesystemName"]),
			SizeBytes:  intValue(info["TotalSize"]),
			Internal:   boolValue(info["Internal"]),
		}
		listDisk := partitionData[id]
		device.Scheme = stringValue(listDisk["Content"])
		partitions, _ := listDisk["Partitions"].([]any)
		if len(partitions) > 0 {
			if part, ok := partitions[0].(map[string]any); ok {
				device.VolumeID = stringValue(part["DeviceIdentifier"])
				device.Name = firstNonEmpty(stringValue(part["VolumeName"]), device.Name)
				device.MountPoint = firstNonEmpty(stringValue(part["MountPoint"]), device.MountPoint)
				device.Filesystem = firstNonEmpty(stringValue(part["FilesystemName"]), stringValue(part["Content"]), device.Filesystem)
			}
		}
		if device.SafeTarget() {
			devices = append(devices, device)
		}
	}
	return devices, nil
}

// MountPoint reports where the volume with the given identifier is currently
// mounted, or "" when it is not mounted.
func MountPoint(ctx context.Context, runner Runner, identifier string) (string, error) {
	data, err := runner.Output(ctx, "diskutil", "info", "-plist", identifier)
	if err != nil {
		return "", err
	}
	info, err := parsePlist(data)
	if err != nil {
		return "", err
	}
	return stringValue(info["MountPoint"]), nil
}

func parsePlist(data []byte) (map[string]any, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if ok && start.Name.Local == "dict" {
			value, err := parseDict(decoder)
			return value, err
		}
	}
}

func parseDict(decoder *xml.Decoder) (map[string]any, error) {
	result := map[string]any{}
	var key string
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Local == "key" {
				if err := decoder.DecodeElement(&key, &t); err != nil {
					return nil, err
				}
				continue
			}
			value, err := parseValue(decoder, t)
			if err != nil {
				return nil, err
			}
			result[key] = value
		case xml.EndElement:
			if t.Name.Local == "dict" {
				return result, nil
			}
		}
	}
}

func parseValue(decoder *xml.Decoder, start xml.StartElement) (any, error) {
	switch start.Name.Local {
	case "dict":
		return parseDict(decoder)
	case "array":
		var values []any
		for {
			token, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			switch t := token.(type) {
			case xml.StartElement:
				value, err := parseValue(decoder, t)
				if err != nil {
					return nil, err
				}
				values = append(values, value)
			case xml.EndElement:
				if t.Name.Local == "array" {
					return values, nil
				}
			}
		}
	case "true":
		if err := decoder.Skip(); err != nil {
			return nil, err
		}
		return true, nil
	case "false":
		if err := decoder.Skip(); err != nil {
			return nil, err
		}
		return false, nil
	case "integer":
		var value string
		if err := decoder.DecodeElement(&value, &start); err != nil {
			return nil, err
		}
		number, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return number, nil
	default:
		var value string
		if err := decoder.DecodeElement(&value, &start); err != nil {
			return nil, err
		}
		return value, nil
	}
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func intValue(value any) int64 {
	result, _ := value.(int64)
	return result
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
