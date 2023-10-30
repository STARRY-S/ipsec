module github.com/rancher/ipsec

go 1.21

replace github.com/Sirupsen/logrus => github.com/sirupsen/logrus v1.9.3

replace github.com/codegangsta/cli => github.com/urfave/cli v1.22.14

require (
	github.com/Sirupsen/logrus v1.9.3
	github.com/bronze1man/goStrongswanVici v0.0.0-20221114103242-3f6dc524986c
	github.com/codegangsta/cli v0.0.0-00010101000000-000000000000
	github.com/mdlayher/arp v0.0.0-20161003162651-d035564bbb23
	github.com/mdlayher/ethernet v0.0.0-20220221185849-529eae5b6118
	github.com/rancher/go-rancher-metadata v0.0.0-20180111235023-489c123d146f
	github.com/rancher/log v0.1.2
	github.com/rancher/plugin-manager v0.8.8
	github.com/vishvananda/netlink v1.1.0
)

require (
	github.com/cpuguy83/go-md2man/v2 v2.0.2 // indirect
	github.com/josharian/native v1.0.0 // indirect
	github.com/konsorten/go-windows-terminal-sequences v1.0.1 // indirect
	github.com/mdlayher/packet v1.0.0 // indirect
	github.com/mdlayher/raw v0.1.0 // indirect
	github.com/mdlayher/socket v0.2.1 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/sirupsen/logrus v1.4.2 // indirect
	github.com/vishvananda/netns v0.0.0-20191106174202-0a2b9b5464df // indirect
	golang.org/x/net v0.0.0-20190603091049-60506f45cf65 // indirect
	golang.org/x/sync v0.0.0-20210220032951-036812b2e83c // indirect
	golang.org/x/sys v0.0.0-20220715151400-c0bba94af5f8 // indirect
)
