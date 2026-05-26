module github.com/chrispian/cerberus

go 1.26.3

require (
	github.com/cloudflare/cloudflare-go/v4 v4.6.0
	github.com/digitalocean/godo v1.180.0
	github.com/fsnotify/fsnotify v1.9.0
	github.com/google/go-github/v72 v72.0.0
	github.com/hollis-labs/go-apppaths v0.1.0
	github.com/hollis-labs/go-mcp v0.0.0
	github.com/hollis-labs/go-webui v0.1.0
	github.com/hollis-labs/plugin-sdk v0.3.1
	github.com/spf13/cobra v1.10.2
	github.com/zalando/go-keyring v0.2.8
	golang.org/x/crypto v0.52.0
	golang.org/x/sys v0.45.0
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.48.0
)

require (
	github.com/adrg/xdg v0.5.3 // indirect
	github.com/danieljoos/wincred v1.2.3 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/godbus/dbus/v5 v5.2.2 // indirect
	github.com/google/go-querystring v1.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/hashicorp/go-retryablehttp v0.7.7 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	github.com/tidwall/gjson v1.14.4 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	golang.org/x/oauth2 v0.27.0 // indirect
	golang.org/x/time v0.6.0 // indirect
	golang.org/x/tools v0.44.0 // indirect
	modernc.org/libc v1.70.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

replace github.com/hollis-labs/go-mcp => ../../libs/go-mcp
