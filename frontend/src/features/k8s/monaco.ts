// Monaco bootstrap: serve the editor from the local npm package instead
// of the default CDN loader (the console must work in offline/private
// networks). Import the core API and YAML support here. JSON language services
// are loaded only if an operator switches the manifest editor to JSON.
import { loader } from '@monaco-editor/react'
import * as monaco from 'monaco-editor/editor/editor.api'
import editorWorker from 'monaco-editor/editor/editor.worker?worker'
import jsonWorker from 'monaco-editor/language/json/json.worker?worker'
import 'monaco-editor/languages/definitions/yaml/register'

self.MonacoEnvironment = {
  getWorker(_id: string, label: string) {
    return label === 'json' ? new jsonWorker() : new editorWorker()
  },
}

loader.config({ monaco })
