%define _name jabridge

Name:           %{_name}
Version:        %{?version}%{!?version:1.0.0}
Release:        %{?release}%{!?release:1}%{?dist}
Summary:        Native-Go Jabra device manager for Linux
License:        Apache-2.0
URL:            https://github.com/Watchdog0x/jabridge
Source0:        %{_name}-%{version}.tar.gz

BuildRequires:  golang

%description
Jabridge provides a terminal interface, daemon, and firmware inspection tools
for Jabra headsets and dongles without libjabra.so or CGo. Hardware-write
features remain experimental and disabled by default.

%prep
%setup -q -n %{_name}-%{version}

%build
mkdir -p dist/bin
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/bin/jabridge .

%install
rm -rf %{buildroot}

install -Dm755 dist/bin/jabridge %{buildroot}/usr/bin/jabridge
install -Dm644 internal/completion/jabridge.bash %{buildroot}/usr/share/bash-completion/completions/jabridge

%files
%license LICENSE
/usr/bin/jabridge
/usr/share/bash-completion/completions/jabridge

%changelog
* Thu Sep 03 2026 Jabridge maintainers - 1.0.0-1
- Native-Go rewrite preview
