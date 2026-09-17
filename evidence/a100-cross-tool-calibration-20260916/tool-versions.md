# Tool identities

| Tool | Verified identity | Evidence |
|---|---|---|
| Slentore | `v0.0.0-20260830170145-4a573b104970`; revision `4a573b104970d7300ebebd136548e0c679fd4f74` | version file, run metadata, recovery log |
| vLLM server | 0.26.0 | environment capture and server provenance |
| vLLM bench | 0.26.0 | isolated benchmark environment install/assertion and result logs |
| NVIDIA AIPerf | 0.12.0 | profile JSON and version log |
| GuideLLM | 0.8.0.dev14; commit `d3a6da9d5055582cbafbf4e09bf05ba7937029b5` | result metadata, install script, recovery-kit record |

The preserved final procedure installed the fixed GuideLLM development commit above. The archive does not preserve enough evidence to independently establish the reported earlier stable-build failure or its cause, so that incident is not asserted here.
