module github.com/solutionsunity/suctl/modules/suctl-mod-os

go 1.23

require github.com/coreos/go-systemd/v22 v22.5.0

require (
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/solutionsunity/suctl/sdk v0.0.0
)

replace github.com/solutionsunity/suctl/sdk => ../../sdk
