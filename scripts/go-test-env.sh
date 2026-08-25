#!/usr/bin/env sh
# 本文件只配置本项目的本地Go Test/Delve环境，所有工具和缓存都位于数据盘。
export PATH="/mnt/data0/tools/go/bin:/mnt/data0/tools/bin:${PATH}"
export GOMODCACHE="/mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo/.cache/go-mod"
export GOCACHE="/mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo/.cache/go-build"
export KUBEBUILDER_ASSETS="/mnt/data0/volcano-scheduler/ngd-ngg-scheduling-demo/.cache/envtest/1.35.5"
export GOMAXPROCS="2"
