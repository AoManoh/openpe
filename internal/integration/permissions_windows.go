//go:build windows

package integration

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// restrictDescriptorPermissions 把 descriptor DACL 设为受保护且仅当前
// 用户可读写。Windows 的 chmod(0600) 只影响只读属性，不能提供 POSIX
// owner-only 语义，因此 ACL 设置失败必须让 lifecycle 启动失败。
func restrictDescriptorPermissions(destinationPath string, tempPath string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	entries := []windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.DELETE | windows.WRITE_DAC,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	info, infoErr := windows.GetNamedSecurityInfo(
		destinationPath,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION,
	)
	if infoErr != nil && !errors.Is(infoErr, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(infoErr, windows.ERROR_PATH_NOT_FOUND) {
		return fmt.Errorf("inspect existing descriptor owner: %w", infoErr)
	}
	if infoErr == nil && info != nil {
		owner, _, ownerErr := info.Owner()
		if ownerErr != nil {
			return ownerErr
		}
		if owner == nil || !owner.Equals(user.User.Sid) {
			return fmt.Errorf("existing descriptor owner is not the current user")
		}
	}
	return windows.SetNamedSecurityInfo(
		tempPath,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user.User.Sid,
		nil,
		acl,
		nil,
	)
}

func validateDescriptorPermissions(path string) error {
	sd, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	if sd == nil {
		return fmt.Errorf("descriptor has no security descriptor")
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(user.User.Sid) {
		return fmt.Errorf("descriptor owner is not the current user")
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("descriptor DACL is not protected")
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if dacl == nil || dacl.AceCount != 1 {
		return fmt.Errorf("descriptor DACL must contain exactly one ACE")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		return err
	}
	if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
		return fmt.Errorf("descriptor DACL entry is not a direct allow ACE")
	}
	required := windows.ACCESS_MASK(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE)
	if ace.Mask&required != required {
		return fmt.Errorf("descriptor DACL does not grant current-user read/write")
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !sid.Equals(user.User.Sid) {
		return fmt.Errorf("descriptor DACL is not owned by the current user")
	}
	return nil
}
