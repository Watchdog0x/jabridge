package main

import (
	"fmt"
	"testing"
)

func TestReturnCode(t *testing.T) {
	tests := []struct {
		name    string
		code    int
		wantErr error
		wantNil bool
	}{
		{"code 0 returns nil", 0, nil, true},
		{"code 1 ErrDeviceUnknown", 1, ErrDeviceUnknown, false},
		{"code 2 ErrDeviceInvalid", 2, ErrDeviceInvalid, false},
		{"code 3 ErrNotSupported", 3, ErrNotSupported, false},
		{"code 4 ErrReturnParameterFail", 4, ErrReturnParameterFail, false},
		{"code 5 ErrProtectedSettingWrite", 5, ErrProtectedSettingWrite, false},
		{"code 6 ErrNoInformation", 6, ErrNoInformation, false},
		{"code 7 ErrNetworkRequestFail", 7, ErrNetworkRequestFail, false},
		{"code 8 ErrDeviceWriteFail", 8, ErrDeviceWriteFail, false},
		{"code 9 ErrDeviceReadFails", 9, ErrDeviceReadFails, false},
		{"code 10 ErrNoFactorySupported", 10, ErrNoFactorySupported, false},
		{"code 11 ErrSystemError", 11, ErrSystemError, false},
		{"code 12 ErrDeviceBadState", 12, ErrDeviceBadState, false},
		{"code 13 ErrFileWriteFail", 13, ErrFileWriteFail, false},
		{"code 14 ErrFileAlreadyExists", 14, ErrFileAlreadyExists, false},
		{"code 15 ErrFileNotAccessible", 15, ErrFileNotAccessible, false},
		{"code 16 ErrFirmwareUpToDate", 16, ErrFirmwareUpToDate, false},
		{"code 17 ErrFirmwareAvailable", 17, ErrFirmwareAvailable, false},
		{"code 18 ErrReturnAsync", 18, ErrReturnAsync, false},
		{"code 19 ErrInvalidAuthorization", 19, ErrInvalidAuthorization, false},
		{"code 20 ErrFWUApplicationNotAvailable", 20, ErrFWUApplicationNotAvailable, false},
		{"code 21 ErrDeviceAlreadyConnected", 21, ErrDeviceAlreadyConnected, false},
		{"code 22 ErrDeviceNotConnected", 22, ErrDeviceNotConnected, false},
		{"code 23 ErrCannotClearDeviceConnected", 23, ErrCannotClearDeviceConnected, false},
		{"code 24 ErrDeviceRebooted", 24, ErrDeviceRebooted, false},
		{"code 25 ErrUploadAlreadyInProgress", 25, ErrUploadAlreadyInProgress, false},
		{"code 26 ErrDownloadAlreadyInProgress", 26, ErrDownloadAlreadyInProgress, false},
		{"code 27 ErrSdkTooOldForFwUpdate", 27, ErrSdkTooOldForFwUpdate, false},
		{"code 28 ErrNoOtaUpdateSupport", 28, ErrNoOtaUpdateSupport, false},
		{"code 29 ErrNonJabraDeviceDetectionDisabled", 29, ErrNonJabraDeviceDetectionDisabled, false},
		{"code 30 ErrDeviceLock", 30, ErrDeviceLock, false},
		{"code 31 ErrDeviceNotLock", 31, ErrDeviceNotLock, false},
		{"code 32 ErrReturnTimeout", 32, ErrReturnTimeout, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := returnCode(tt.code)
			if tt.wantNil {
				if got != nil {
					t.Errorf("returnCode(%d) = %v, want nil", tt.code, got)
				}
				return
			}
			if got != tt.wantErr {
				t.Errorf("returnCode(%d) = %v, want %v", tt.code, got, tt.wantErr)
			}
		})
	}

	t.Run("unknown code 99 returns ReturnCodeError", func(t *testing.T) {
		got := returnCode(99)
		if got == nil {
			t.Fatal("returnCode(99) = nil, want non-nil error")
		}
		rce, ok := got.(*ReturnCodeError)
		if !ok {
			t.Fatalf("returnCode(99) returned %T, want *ReturnCodeError", got)
		}
		if rce.code != 99 {
			t.Errorf("ReturnCodeError.code = %d, want 99", rce.code)
		}
		if rce.message != "Unknown return code" {
			t.Errorf("ReturnCodeError.message = %q, want %q", rce.message, "Unknown return code")
		}
	})

	t.Run("negative code -1 returns ReturnCodeError", func(t *testing.T) {
		got := returnCode(-1)
		if got == nil {
			t.Fatal("returnCode(-1) = nil, want non-nil error")
		}
		rce, ok := got.(*ReturnCodeError)
		if !ok {
			t.Fatalf("returnCode(-1) returned %T, want *ReturnCodeError", got)
		}
		if rce.code != -1 {
			t.Errorf("ReturnCodeError.code = %d, want -1", rce.code)
		}
		if rce.message != "Unknown return code" {
			t.Errorf("ReturnCodeError.message = %q, want %q", rce.message, "Unknown return code")
		}
	})
}

func TestCheckErrorStatus(t *testing.T) {
	tests := []struct {
		name    string
		code    errorStatusCode
		wantErr error
		wantNil bool
	}{
		{"code 0 returns nil", 0, nil, true},
		{"code 1 ErrSSLError", 1, ErrSSLError, false},
		{"code 2 ErrCertError", 2, ErrCertError, false},
		{"code 3 ErrNetworkError", 3, ErrNetworkError, false},
		{"code 4 ErrDownloadError", 4, ErrDownloadError, false},
		{"code 5 ErrParseError", 5, ErrParseError, false},
		{"code 6 ErrOtherError", 6, ErrOtherError, false},
		{"code 7 ErrDeviceInfoError", 7, ErrDeviceInfoError, false},
		{"code 8 ErrFileNotAccessibleStatus", 8, ErrFileNotAccessibleStatus, false},
		{"code 9 ErrFileNotCompatible", 9, ErrFileNotCompatible, false},
		{"code 10 ErrDeviceNotFound", 10, ErrDeviceNotFound, false},
		{"code 11 ErrParameterFail", 11, ErrParameterFail, false},
		{"code 12 ErrAuthorizationFailed", 12, ErrAuthorizationFailed, false},
		{"code 13 ErrFileNotAvailable", 13, ErrFileNotAvailable, false},
		{"code 14 ErrConfigParseError", 14, ErrConfigParseError, false},
		{"code 15 ErrSetSettingsFail", 15, ErrSetSettingsFail, false},
		{"code 16 ErrDeviceReboot", 16, ErrDeviceReboot, false},
		{"code 17 ErrDeviceReadFail", 17, ErrDeviceReadFail, false},
		{"code 18 ErrDeviceNotReady", 18, ErrDeviceNotReady, false},
		{"code 19 ErrFilePartiallyCompatible", 19, ErrFilePartiallyCompatible, false},
		{"code 20 ErrSdkTooOldForFwUpdateError", 20, ErrSdkTooOldForFwUpdateError, false},
		{"code 21 ErrUpdateIsNotReady", 21, ErrUpdateIsNotReady, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkErrorStatus(tt.code)
			if tt.wantNil {
				if got != nil {
					t.Errorf("checkErrorStatus(%d) = %v, want nil", tt.code, got)
				}
				return
			}
			if got != tt.wantErr {
				t.Errorf("checkErrorStatus(%d) = %v, want %v", tt.code, got, tt.wantErr)
			}
		})
	}

	t.Run("unknown code 99 returns JabraErrorStatusCode", func(t *testing.T) {
		got := checkErrorStatus(99)
		if got == nil {
			t.Fatal("checkErrorStatus(99) = nil, want non-nil error")
		}
		jesc, ok := got.(*JabraErrorStatusCode)
		if !ok {
			t.Fatalf("checkErrorStatus(99) returned %T, want *JabraErrorStatusCode", got)
		}
		if jesc.code != 99 {
			t.Errorf("JabraErrorStatusCode.code = %d, want 99", jesc.code)
		}
		if jesc.message != "Unknown error status" {
			t.Errorf("JabraErrorStatusCode.message = %q, want %q", jesc.message, "Unknown error status")
		}
	})
}

func TestReturnCodeError_Error(t *testing.T) {
	tests := []struct {
		name    string
		err     *ReturnCodeError
		want    string
	}{
		{
			"sentinel error format",
			ErrDeviceUnknown,
			"Error 1: The device is not known",
		},
		{
			"unknown code format",
			&ReturnCodeError{99, "Unknown return code"},
			"Error 99: Unknown return code",
		},
		{
			"zero code format",
			ErrReturnOk,
			"Error 0: Success",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestJabraErrorStatusCode_Error(t *testing.T) {
	tests := []struct {
		name string
		err  *JabraErrorStatusCode
		want string
	}{
		{
			"sentinel error format",
			ErrSSLError,
			"Jabra_ErrorStatus 1: SSL Handshake failed. Please contact your administrator",
		},
		{
			"unknown code format",
			&JabraErrorStatusCode{99, "Unknown error status"},
			"Jabra_ErrorStatus 99: Unknown error status",
		},
		{
			"zero code format",
			ErrNoError,
			"Jabra_ErrorStatus 0: No Error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReturnCodeSentinelIdentity(t *testing.T) {
	sentinels := []struct {
		code int
		want *ReturnCodeError
	}{
		{1, ErrDeviceUnknown},
		{2, ErrDeviceInvalid},
		{3, ErrNotSupported},
		{4, ErrReturnParameterFail},
		{5, ErrProtectedSettingWrite},
		{6, ErrNoInformation},
		{7, ErrNetworkRequestFail},
		{8, ErrDeviceWriteFail},
		{9, ErrDeviceReadFails},
		{10, ErrNoFactorySupported},
		{11, ErrSystemError},
		{12, ErrDeviceBadState},
		{13, ErrFileWriteFail},
		{14, ErrFileAlreadyExists},
		{15, ErrFileNotAccessible},
		{16, ErrFirmwareUpToDate},
		{17, ErrFirmwareAvailable},
		{18, ErrReturnAsync},
		{19, ErrInvalidAuthorization},
		{20, ErrFWUApplicationNotAvailable},
		{21, ErrDeviceAlreadyConnected},
		{22, ErrDeviceNotConnected},
		{23, ErrCannotClearDeviceConnected},
		{24, ErrDeviceRebooted},
		{25, ErrUploadAlreadyInProgress},
		{26, ErrDownloadAlreadyInProgress},
		{27, ErrSdkTooOldForFwUpdate},
		{28, ErrNoOtaUpdateSupport},
		{29, ErrNonJabraDeviceDetectionDisabled},
		{30, ErrDeviceLock},
		{31, ErrDeviceNotLock},
		{32, ErrReturnTimeout},
	}

	for _, tt := range sentinels {
		t.Run(fmt.Sprintf("code %d pointer identity", tt.code), func(t *testing.T) {
			got := returnCode(tt.code)
			// Verify pointer equality -- returnCode must return the exact sentinel, not a copy.
			gotPtr, ok := got.(*ReturnCodeError)
			if !ok {
				t.Fatalf("returnCode(%d) returned %T, want *ReturnCodeError", tt.code, got)
			}
			if gotPtr != tt.want {
				t.Errorf("returnCode(%d) returned pointer %p, want sentinel pointer %p", tt.code, gotPtr, tt.want)
			}
		})
	}
}
