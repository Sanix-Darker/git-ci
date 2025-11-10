#!/bin/bash

apt-get update -y
apt-get install \
  build-essential \
  libbtrfs-dev \
  libgpgme-dev \
  libseccomp-dev \
  pkg-config -yqq
