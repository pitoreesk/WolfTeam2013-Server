# Native Windows Server / VDS startup template for wtserver.exe
# Copy this file to your server directory as start.ps1 and fill in your values.
# NEVER commit your real start.ps1 with production passwords or IPs to Git.

$ErrorActionPreference = 'Stop'

# Replace 203.0.113.10 with your server's public IPv4 (or 127.0.0.1 for local play)
$serverIP = "203.0.113.10"

$env:WT_DATABASE_URL       = "postgres://wolfteam:CHANGE_ME_STRONG_PASSWORD@127.0.0.1:5432/wolfteam?sslmode=disable"
$env:WT_LISTEN_HOST        = "0.0.0.0"
$env:WT_ADVERTISED_IP      = $serverIP
$env:WT_PUBLIC_IP          = $serverIP
$env:WT_PEER_ENDPOINT_MODE = "2"

# Protocol mode:
#   false = 2013 TR client
#   true  = September 2012 TR client
$env:WT_CLIENT_2012        = "false"
$env:WT_WPACK_ENABLED      = "true"

# Service ports
$env:WT_BROKER_PORT        = "40706"
$env:WT_GAME_PORT          = "40707"
$env:WT_BOT_PEER_PORT_BASE = "40708"
$env:WT_BUDDY_PORT         = "40709"
$env:WT_RELAY_PORT         = "40710"
$env:WT_RELAY_PORT_BASE    = "41000"
$env:WT_RANKING_PORT       = "40719"
$env:WT_PATCH_ENABLED      = "true"
$env:WT_PATCH_PORT         = "8080"
$env:WT_LOGIN_PORT         = "8444"

# Data catalogs (placed in the same directory as wtserver.exe)
$baseDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$env:WT_SHOPDATA_PATH      = Join-Path $baseDir "shopdata.dat"
$env:WT_LUCKYBOXDATA_PATH  = Join-Path $baseDir "bighitboxitem.dat"
$env:WT_NOTICE_WEB_DIR     = Join-Path $baseDir "notice"

$logFile = Join-Path $baseDir "wtserver.log"
Set-Location $baseDir

& (Join-Path $baseDir "wtserver.exe") *>> $logFile
