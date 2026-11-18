module artemis-ios

go 1.26.0

require (
	github.com/danielpaulus/go-ios v1.3.3-0.20261103131045-6910f3b585ad
	github.com/google/uuid v1.1.2
	gvisor.dev/gvisor v0.0.0-20240405191320-0878b34101b5
	howett.net/plist v1.0.1
)

require (
	github.com/Masterminds/semver v1.5.0 // indirect
	github.com/cenkalti/backoff v2.2.1+incompatible // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/grandcat/zeroconf v1.0.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/miekg/dns v1.1.57 // indirect
	github.com/pierrec/lz4 v2.6.1+incompatible // indirect
	go.mozilla.org/pkcs7 v0.9.0 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/mod v0.35.0 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	golang.org/x/time v0.5.0 // indirect
	golang.org/x/tools v0.44.0 // indirect
	google.golang.org/protobuf v1.32.0 // indirect
	software.sslmate.com/src/go-pkcs12 v0.7.2 // indirect
)

// This revision verifies the device's TLS key against its existing pair record.
replace github.com/danielpaulus/go-ios => github.com/DIYgod/go-ios v0.0.0-20261108182015-53a1fc8154fc
