# Makefile for juju-core.
PROJECT=launchpad.net/juju-core

# Default target.  Compile, just to see if it will.
build:
	go build $(PROJECT)/...

# Run tests.
check:
	go test $(PROJECT)/...

# Reformat the source files.
format:
	go fmt $(PROJECT)/...

# Install packages required to develop Juju and run tests.
install-dependencies:
	@echo Adding juju PPAs for golang and mongodb-server
	@sudo apt-add-repository ppa:juju/golang
    # XXX - this should be changed to devel?
	@sudo apt-add-repository ppa:juju/experimental
	@sudo apt-get update
	@echo Installing dependencies
	@sudo apt-get install golang mongodb-server build-essential bzr \
		zip git-core mercurial distro-info-data
	@if [ -z "$(GOPATH)" ]; then \
		echo; \
		echo "You need to set up a GOPATH.  See the README file."; \
	fi

# Invoke gofmt's "simplify" option to streamline the source code.
simplify:
	find "$(GOPATH)/src/$(PROJECT)/" -name \*.go | xargs gofmt -w -s


.PHONY: build check format install-dependencies simplify
