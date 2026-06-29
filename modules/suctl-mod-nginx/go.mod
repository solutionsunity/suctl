module github.com/solutionsunity/suctl/modules/suctl-mod-nginx

go 1.23.0

require github.com/nginxinc/nginx-go-crossplane v0.4.88

require (
	github.com/jstemmer/go-junit-report v1.0.0 // indirect
	github.com/solutionsunity/suctl/sdk v0.0.0
	golang.org/x/mod v0.24.0 // indirect
	golang.org/x/sync v0.13.0 // indirect
	golang.org/x/tools v0.32.0 // indirect
)

replace github.com/solutionsunity/suctl/sdk => ../../sdk
