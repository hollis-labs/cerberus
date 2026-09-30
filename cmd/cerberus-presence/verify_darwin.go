//go:build darwin && cgo

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework LocalAuthentication -framework Foundation
#import <LocalAuthentication/LocalAuthentication.h>
#include <stdlib.h>
#include <string.h>

// cerb_presence evaluates the device-owner policy (Touch ID, or the
// account password) and waits for the answer. It returns 0 verified, 1
// refused, 2 not available; *msg is a malloc'd reason, or NULL.
static int cerb_presence(const char *reason, char **msg) {
	*msg = NULL;
	LAContext *ctx = [[LAContext alloc] init];
	NSError *err = nil;
	if (![ctx canEvaluatePolicy:LAPolicyDeviceOwnerAuthentication error:&err]) {
		if (err != nil) {
			*msg = strdup([[err localizedDescription] UTF8String]);
		}
		return 2;
	}
	dispatch_semaphore_t done = dispatch_semaphore_create(0);
	__block int rc = 1;
	__block NSString *why = nil;
	[ctx evaluatePolicy:LAPolicyDeviceOwnerAuthentication
	    localizedReason:[NSString stringWithUTF8String:reason]
	              reply:^(BOOL ok, NSError *e) {
		if (ok) {
			rc = 0;
		} else if (e != nil) {
			why = [e localizedDescription];
			if (e.code == LAErrorNotInteractive) {
				rc = 2;
			}
		}
		dispatch_semaphore_signal(done);
	}];
	dispatch_semaphore_wait(done, DISPATCH_TIME_FOREVER);
	if (why != nil) {
		*msg = strdup([why UTF8String]);
	}
	return rc;
}
*/
import "C"

import "unsafe"

func verify(reason string) (int, string) {
	cReason := C.CString(reason)
	defer C.free(unsafe.Pointer(cReason))
	var cMsg *C.char
	rc := int(C.cerb_presence(cReason, &cMsg))
	msg := ""
	if cMsg != nil {
		msg = C.GoString(cMsg)
		C.free(unsafe.Pointer(cMsg))
	}
	return rc, msg
}
