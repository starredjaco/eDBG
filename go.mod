module eDBG

go 1.23.0

require (
	github.com/c-bata/go-prompt v0.2.6
	github.com/casbin/govaluate v1.7.0
	github.com/cilium/ebpf v0.17.3
	github.com/gojue/ebpfmanager v0.4.6
	github.com/inconshreveable/go-update v0.0.0-20160112193335-8152e7eb6ccf
	github.com/shuLhan/go-bindata v4.0.0+incompatible
	golang.org/x/arch v0.15.0
	golang.org/x/exp v0.0.0-20230224173230-c95f2b4c22f2
	golang.org/x/sys v0.30.0
)

replace github.com/cilium/ebpf => ./ebpf

replace github.com/gojue/ebpfmanager => ./ebpfmanager

require (
	github.com/avast/retry-go v3.0.0+incompatible // indirect
	github.com/florianl/go-tc v0.4.0 // indirect
	github.com/google/go-cmp v0.6.0 // indirect
	github.com/hashicorp/errwrap v1.0.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/josharian/native v1.1.0 // indirect
	github.com/mattn/go-colorable v0.1.7 // indirect
	github.com/mattn/go-isatty v0.0.12 // indirect
	github.com/mattn/go-runewidth v0.0.9 // indirect
	github.com/mattn/go-tty v0.0.3 // indirect
	github.com/mdlayher/netlink v1.7.2 // indirect
	github.com/mdlayher/socket v0.4.1 // indirect
	github.com/pkg/term v1.2.0-beta.2 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/vishvananda/netlink v1.1.0 // indirect
	github.com/vishvananda/netns v0.0.0-20191106174202-0a2b9b5464df // indirect
	golang.org/x/net v0.36.0 // indirect
	golang.org/x/sync v0.1.0 // indirect
)
