# Text the model sees (system prompts, messages, tool definitions) lives in
# ..\prompts, shared with the Go engine so both send identical text. Format
# and rules: prompts\README.md.

$script:PromptDir = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\prompts'))
$script:PromptCache = @{}

# {{name}} is replaced by $Values.name; {{#name}}...{{/name}} is kept when the
# value is set (not $null, '' or $false), {{^name}}...{{/name}} when it is not.
# Only lower-case names are placeholders, so install.md's {{REPO_DIR}} stays.
# Inserted values are not scanned again.
function Format-Prompt([string]$Template, $Values = @{}) {
    $get = { param($n) if (-not $Values.Contains($n)) { throw "prompt value '$n' is not set" }; $Values[$n] }
    $section = '\{\{([#^])([a-z][a-z0-9_]*)\}\}((?:(?!\{\{[#^/][a-z][a-z0-9_]*\}\}).)*?)\{\{/\2\}\}'
    while (($m = [regex]::Match($Template, $section, 'Singleline')).Success) {
        $v = & $get $m.Groups[2].Value
        $on = $null -ne $v -and -not ($v -is [bool] -and -not $v) -and "$v" -ne ''
        $keep = if ($m.Groups[1].Value -eq '#') { $on } else { -not $on }
        $Template = $Template.Substring(0, $m.Index) + $(if ($keep) { $m.Groups[3].Value } else { '' }) + $Template.Substring($m.Index + $m.Length)
    }
    if ($Template -match '\{\{[#^/][a-z][a-z0-9_]*\}\}') { throw "prompt section $($Matches[0]) is not closed or not opened" }
    $sb = [Text.StringBuilder]::new(); $at = 0
    foreach ($m in [regex]::Matches($Template, '\{\{([a-z][a-z0-9_]*)\}\}')) {
        [void]$sb.Append($Template, $at, $m.Index - $at).Append("$(& $get $m.Groups[1].Value)")
        $at = $m.Index + $m.Length
    }
    [void]$sb.Append($Template, $at, $Template.Length - $at)
    $sb.ToString()
}

function Read-PromptFile([string]$Name) {
    if (-not $script:PromptCache.Contains($Name)) {
        $text = [IO.File]::ReadAllText((Join-Path $script:PromptDir $Name), [Text.UTF8Encoding]::new($false))
        $script:PromptCache[$Name] = $(if ($Name -like '*.json') { $text | ConvertFrom-Json } elseif ($text.EndsWith("`n")) { $text.Substring(0, $text.Length - 1) } else { $text })
    }
    $script:PromptCache[$Name]
}

# A system prompt or other long text: prompts\<Name>.md without its final newline.
function Get-Prompt([string]$Name, $Values = @{}) { Format-Prompt (Read-PromptFile "$Name.md") $Values }

# A short message from prompts\messages.json.
function Get-PromptMessage([string]$Name, $Values = @{}) {
    $t = (Read-PromptFile 'messages.json').$Name
    if ($null -eq $t) { throw "prompts\messages.json has no message '$Name'" }
    Format-Prompt $t $Values
}

# JSON objects become hashtables built like a @{...} literal with the same
# keys in file order (same size and comparer), so requests serialize as they
# did when the definitions were written in code.
function ConvertTo-PromptHashtable($Node, $Values) {
    if ($Node -is [Management.Automation.PSCustomObject]) {
        $props = @($Node.PSObject.Properties)
        $h = [hashtable]::new($props.Count, [StringComparer]::CurrentCultureIgnoreCase)
        foreach ($p in $props) { $h[$p.Name] = ConvertTo-PromptHashtable $p.Value $Values }
        return $h
    }
    if ($Node -is [array]) { return , @($Node | ForEach-Object { ConvertTo-PromptHashtable $_ $Values }) }
    if ($Node -is [string] -and $Node -match '^\{\{([a-z][a-z0-9_]*)\}\}$') {
        if (-not $Values.Contains($Matches[1])) { throw "prompt value '$($Matches[1])' is not set" }
        $v = $Values[$Matches[1]]
        if ($v -is [array]) { return , $v } else { return $v }
    }
    if ($Node -is [string] -and $Node.Contains('{{')) { return Format-Prompt $Node $Values }
    $Node
}

# A fresh copy of one tool definition (prompts\tools.json "tools") or
# parameter schema ("parameters"), with placeholders in its text filled. A
# string that is only "{{name}}" becomes the value itself (an enum's list):
# setting a key after the table is built can change its serialized order.
function Get-PromptTool([string]$Key, $Values = @{}) {
    $t = (Read-PromptFile 'tools.json').tools.$Key
    if ($null -eq $t) { throw "prompts\tools.json has no tool '$Key'" }
    ConvertTo-PromptHashtable $t $Values
}
function Get-PromptParameter([string]$Key, $Values = @{}) {
    $t = (Read-PromptFile 'tools.json').parameters.$Key
    if ($null -eq $t) { throw "prompts\tools.json has no parameter '$Key'" }
    ConvertTo-PromptHashtable $t $Values
}
