// Package erofs provides helpers for EROFS-related image handling.
package erofs

import (
	"fmt"
	"io"
	"os"
)

const (
	max2GbExtentSectors = 0x80000000 >> 9
	sectorsPerTrack     = 63
	numberHeads         = 16
	subformat           = "twoGbMaxExtentFlat"
	adapterType         = "ide"
	hwVersion           = "4"
)

// vmdkDescAddExtent writes extent lines to the writer.
// Each extent line follows the format: RW <count> FLAT "<filename>" <offset>
func vmdkDescAddExtent(w io.Writer, sectors uint64, filename string, offset uint64) error {
	for sectors > 0 {
		count := min(sectors, max2GbExtentSectors)

		_, err := fmt.Fprintf(w, "RW %d FLAT \"%s\" %d\n", count, filename, offset)
		if err != nil {
			return err
		}
		offset += count
		sectors -= count
	}
	return nil
}

// DumpVMDKDescriptor writes a VMDK descriptor to the provided writer.
func DumpVMDKDescriptor(w io.Writer, cid uint32, devices []string) error {
	parentCID := uint32(0xffffffff)

	_, err := fmt.Fprintf(w, `# Disk DescriptorFile
version=1
CID=%08x
parentCID=%08x
createType="%s"

# Extent description
`, cid, parentCID, subformat)
	if err != nil {
		return err
	}

	totalSectors := uint64(0)

	for _, d := range devices {
		fi, err := os.Stat(d)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("device %q does not exist", d)
			}
			return fmt.Errorf("failed to stat device %q: %w", d, err)
		}
		if fi.Size() == 0 {
			return fmt.Errorf("device %q has zero size", d)
		}
		sectors := uint64(fi.Size()) >> 9
		if sectors == 0 {
			return fmt.Errorf("device %q is too small (less than 512 bytes)", d)
		}
		err = vmdkDescAddExtent(w, sectors, d, 0)
		if err != nil {
			return err
		}
		totalSectors += sectors
	}

	cylinders := (totalSectors + sectorsPerTrack*numberHeads - 1) / (sectorsPerTrack * numberHeads)
	_, err = fmt.Fprintf(w, `

# The Disk Data Base
#DDB

ddb.virtualHWVersion = "%s"
ddb.geometry.cylinders = "%d"
ddb.geometry.heads = "%d"
ddb.geometry.sectors = "63"
ddb.adapterType = "%s"
`, hwVersion, cylinders, numberHeads, adapterType)
	if err != nil {
		return err
	}
	return nil
}

// DumpVMDKDescriptorToFile writes a VMDK descriptor to the given file path.
func DumpVMDKDescriptorToFile(vmdkdesc string, cid uint32, devices []string) error {
	f, err := os.Create(vmdkdesc)
	if err != nil {
		return err
	}
	if err := DumpVMDKDescriptor(f, cid, devices); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
