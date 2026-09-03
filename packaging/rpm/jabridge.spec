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
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/Watchdog0x/jabridge/internal/buildinfo.Version=%{version}" -o dist/bin/jabridge .

%install
rm -rf %{buildroot}

install -Dm755 dist/bin/jabridge %{buildroot}%{_bindir}/jabridge
install -Dm644 internal/completion/jabridge.bash %{buildroot}%{_datadir}/bash-completion/completions/jabridge
install -Dm644 dist/jabridge.service %{buildroot}%{_prefix}/lib/systemd/user/jabridge.service
install -Dm644 dist/70-jabridge.rules %{buildroot}%{_prefix}/lib/udev/rules.d/70-jabridge.rules

%files
%license LICENSE
%doc README.md CHANGELOG.md HARDWARE_TESTING.md
%{_bindir}/jabridge
%{_datadir}/bash-completion/completions/jabridge
%{_prefix}/lib/systemd/user/jabridge.service
%{_prefix}/lib/udev/rules.d/70-jabridge.rules

%post
udevadm control --reload-rules >/dev/null 2>&1 || :

%postun
udevadm control --reload-rules >/dev/null 2>&1 || :

%changelog
* Thu Sep 03 2026 Jabridge maintainers - 1.0.0-1
- Native-Go rewrite preview
