$ErrorActionPreference = 'Stop'

$File = '.\router\aurp_peer.go'

if (-not (Test-Path -LiteralPath $File)) {
    throw "Cannot find $File"
}

$Lines = [System.IO.File]::ReadAllLines(
    (Resolve-Path -LiteralPath $File)
)

$Replacements = @(
    # --------------------------------------------------------
    # Conflict 1:
    # Preserve Set24 RD behavior, but pass packet name.
    # Duplicate RD must be ACKed with flags == 0.
    # --------------------------------------------------------
    @(
        "`tif err := p.checkRemoteSeqWithAckFlags(logger, &pkt.TrHeader, `"RD`", 0); err != nil {"
    ),

    # --------------------------------------------------------
    # Conflict 2:
    # Keep Set24's two-level helper, extended with Green25's
    # packetName diagnostic parameter.
    # --------------------------------------------------------
    @(
        'func (p *AURPPeer) checkRemoteSeq(logger *slog.Logger, trheader *aurp.TrHeader, packetName string) error {',
        "`treturn p.checkRemoteSeqWithAckFlags(",
        "`t`tlogger,",
        "`t`ttrheader,",
        "`t`tpacketName,",
        "`t`taurp.RoutingFlagSendZoneInfo,",
        "`t)",
        '}',
        '',
        'func (p *AURPPeer) checkRemoteSeqWithAckFlags(',
        "`tlogger *slog.Logger,",
        "`ttrheader *aurp.TrHeader,",
        "`tpacketName string,",
        "`tduplicateAckFlags aurp.RoutingFlag,",
        ') error {'
    ),

    # --------------------------------------------------------
    # Conflict 3:
    # Keep Green25 diagnostics but use Set24's caller-selected
    # duplicate ACK flags.
    # --------------------------------------------------------
    @(
        "`t`tlogger.Debug(",
        "`t`t`t`"AURP Peer: duplicate sequenced routing packet; retransmitting RI-Ack`",",
        "`t`t`t`"packet`", packetName,",
        "`t`t`t`"packet-seq`", got,",
        "`t`t`t`"expected-seq`", want,",
        "`t`t`t`"action`", `"re-ack-and-drop`",",
        "`t`t)",
        "`t`tif _, err := p.send(p.Transport.NewRIAckPacket(trheader.ConnectionID, trheader.Sequence, duplicateAckFlags)); err != nil {"
    )
)

$Output = New-Object System.Collections.Generic.List[string]

$ConflictIndex = 0
$i = 0

while ($i -lt $Lines.Count) {

    if ($Lines[$i] -match '^<<<<<<< ') {

        if ($ConflictIndex -ge $Replacements.Count) {
            throw "Found more conflicts than expected."
        }

        # Skip everything through the matching >>>>>>> marker.
        while (
            $i -lt $Lines.Count -and
            $Lines[$i] -notmatch '^>>>>>>> '
        ) {
            $i++
        }

        if ($i -ge $Lines.Count) {
            throw "Unterminated Git conflict block."
        }

        foreach ($ReplacementLine in $Replacements[$ConflictIndex]) {
            $Output.Add($ReplacementLine)
        }

        $ConflictIndex++
        $i++

        continue
    }

    $Output.Add($Lines[$i])
    $i++
}

if ($ConflictIndex -ne 3) {
    throw "Expected exactly 3 conflicts; resolved $ConflictIndex."
}

[System.IO.File]::WriteAllLines(
    (Resolve-Path -LiteralPath $File),
    $Output,
    (New-Object System.Text.UTF8Encoding($false))
)

Write-Host ''
Write-Host "Resolved $ConflictIndex conflict blocks."
Write-Host ''

$Remaining = Select-String `
    -Path $File `
    -Pattern '^<<<<<<<|^=======|^>>>>>>>'

if ($Remaining) {
    throw 'Conflict markers remain in aurp_peer.go.'
}

Write-Host 'PASS: no conflict markers remain.'