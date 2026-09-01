%define _name jlink

Name:           %{_name}
Version:        %{?version}%{!?version:0.1.1}
Release:        %{?release}%{!?release:1}%{?dist}
Summary:        Jabra Direct for Linux - manage Jabra headsets and dongles
License:        Apache-2.0
URL:            https://github.com/Watchdog0x/jLink
Source0:        %{_name}-%{version}.tar.gz

Requires:       alsa-lib
Requires:       libcurl

%description
jLink is a TUI (terminal user interface) application for managing Jabra
headsets and Bluetooth dongles on Linux. It provides device discovery,
Bluetooth pairing, battery monitoring, and dongle configuration features.
Think of it as Jabra Direct for Linux.

%prep
%setup -q -n %{_name}-%{version}

%install
rm -rf %{buildroot}

# Install binary
install -Dm755 jLink %{buildroot}/usr/local/bin/jlink

# Install Jabra SDK library
install -Dm644 libjabra.so %{buildroot}/usr/lib/jabra/libjabra.so
ln -rs %{buildroot}/usr/lib/jabra/libjabra.so %{buildroot}/usr/lib/jabra/libjabra.so.1

# Install udev rules
install -dm755 %{buildroot}/etc/udev/rules.d
echo 'ATTRS{idVendor}=="0b0e", MODE="0660", GROUP="users"' > \
    %{buildroot}/etc/udev/rules.d/99-jabra.rules

# Install ldconfig configuration
install -dm755 %{buildroot}/etc/ld.so.conf.d
echo "/usr/lib/jabra" > %{buildroot}/etc/ld.so.conf.d/jabra.conf

%post
/sbin/ldconfig
if command -v udevadm >/dev/null 2>&1; then
    udevadm control --reload-rules || true
    udevadm trigger || true
fi

%postun
/sbin/ldconfig

%files
%license ../../BUILD/%{_name}-%{version}/../../../LICENSE 2>/dev/null || true
/usr/local/bin/jlink
/usr/lib/jabra/libjabra.so
/usr/lib/jabra/libjabra.so.1
/etc/udev/rules.d/99-jabra.rules
/etc/ld.so.conf.d/jabra.conf

%changelog
* %(date "+%a %b %d %Y") jLink maintainers - %{version}-%{release}
- Automated build from release tag
