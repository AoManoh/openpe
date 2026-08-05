//go:build !windows

package integration

func restrictDescriptorPermissions(destinationPath string, tempPath string) error {
	return nil
}

func validateDescriptorPermissions(path string) error {
	return nil
}
