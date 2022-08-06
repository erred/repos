# repos

manage the state of local working repos

[![Go Reference](https://pkg.go.dev/badge/go.seankhliao.com/repos.svg)](https://pkg.go.dev/go.seankhliao.com/repos)
[![License](https://img.shields.io/github/license/seankhliao/repos.svg?style=flat-square)](LICENSE)

install

```sh
go install go.seankhliao.com/repos@latest
```

With the wrapper:
```sh
repos () {
	local out=$(command repos "$@") 
	eval "${out}"
}
```
