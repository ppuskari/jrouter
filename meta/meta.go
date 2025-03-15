/*
   Copyright 2025 Josh Deprez

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package meta

import (
	_ "embed"
	"strings"
)

// Name of the software.
const Name = "jrouter"

//go:embed VERSION
var rawVersion string

// Version is the SemVer version string (without 'v' prefix).
var Version = strings.TrimSpace(rawVersion)

// NameVersion is the full name and version string.
var NameVersion = Name + " v" + Version
