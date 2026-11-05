# SPDX-FileCopyrightText: 2026 groupkit contributors
#
# SPDX-License-Identifier: Apache-2.0

# Evolution's data server and its Python bindings, without a desktop. The
# script in this directory drives the same client code Evolution uses.
FROM debian:trixie-slim

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        dbus dbus-x11 evolution-data-server gir1.2-edataserver-1.2 \
        gir1.2-ecal-2.0 gir1.2-ebook-1.2 gir1.2-ebookcontacts-1.2 \
        gir1.2-icalglib-3.0 python3 python3-gi gnome-keyring libsecret-tools \
    && rm -rf /var/lib/apt/lists/*

RUN useradd --create-home evo
USER evo
WORKDIR /home/evo
