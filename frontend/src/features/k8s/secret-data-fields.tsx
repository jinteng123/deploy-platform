/* eslint-disable react-refresh/only-export-components */
import { useState } from 'react'
import { Eye, EyeOff } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { DataCode, DataFields } from './data-fields'
import type { KvRow } from './kv-rows-editor'

export function decodeSecretData(data: Record<string, string> = {}) {
  return Object.fromEntries(
    Object.entries(data).map(([key, value]) => {
      if (value === '***') throw new Error('未获取到 Secret 原值，请重新加载')
      return [
        key,
        new TextDecoder().decode(
          Uint8Array.from(atob(value), (c) => c.charCodeAt(0))
        ),
      ]
    })
  )
}
const encode = (value: string) =>
  btoa(
    Array.from(new TextEncoder().encode(value), (n) =>
      String.fromCharCode(n)
    ).join('')
  )

export function SecretDataFields({
  type,
  rows,
  onChange,
  editing,
  disabled,
}: {
  type: string
  rows: KvRow[]
  onChange: (rows: KvRow[]) => void
  editing: boolean
  disabled?: boolean
}) {
  const [showPassword, setShowPassword] = useState(false)
  const data = Object.fromEntries(rows.map((r) => [r.key, r.value]))
  const set = (key: string, value: string) => {
    onChange(
      rows.some((r) => r.key === key)
        ? rows.map((r) => (r.key === key ? { ...r, value } : r))
        : [...rows, { key, value }]
    )
  }
  if (type === 'kubernetes.io/tls') {
    return (
      <div className='space-y-5 border border-[#ebeef5] bg-white p-5'>
        {(['tls.crt', 'tls.key'] as const).map((key) => (
          <div key={key} className='space-y-2'>
            <Label>{key === 'tls.crt' ? '证书 tls.crt' : '私钥 tls.key'}</Label>
            {editing && (
              <Input
                type='file'
                aria-label={`上传 ${key}`}
                disabled={disabled}
                className='max-w-sm rounded text-xs'
                onChange={async (e) => {
                  const file = e.target.files?.[0]
                  if (file) set(key, await file.text())
                  e.target.value = ''
                }}
              />
            )}
            <DataCode
              value={data[key] ?? ''}
              label={key}
              disabled={disabled}
              onChange={editing ? (value) => set(key, value) : undefined}
            />
          </div>
        ))}
      </div>
    )
  }
  const fallback = (
    <DataFields
      rows={rows}
      onChange={onChange}
      editing={editing}
      disabled={disabled}
    />
  )
  if (type !== 'kubernetes.io/dockerconfigjson') return fallback
  let config: {
    auths: Record<string, Record<string, unknown>>
    [key: string]: unknown
  }
  try {
    config = JSON.parse(data['.dockerconfigjson'] ?? '{}')
    if (
      !config ||
      typeof config !== 'object' ||
      !config.auths ||
      typeof config.auths !== 'object' ||
      Array.isArray(config.auths)
    )
      return fallback
  } catch {
    return fallback
  }
  const updateAuth = (server: string, field: string, value: string) => {
    const auths = { ...config.auths }
    if (field === 'server') {
      if (
        value !== server &&
        Object.prototype.hasOwnProperty.call(auths, value)
      )
        return
      const entry = auths[server]
      delete auths[server]
      auths[value] = entry
    } else {
      const entry = { ...auths[server], [field]: value }
      entry.auth = encode(`${entry.username ?? ''}:${entry.password ?? ''}`)
      auths[server] = entry
    }
    set('.dockerconfigjson', JSON.stringify({ ...config, auths }))
  }
  return (
    <div className='space-y-5 border border-[#ebeef5] bg-white p-5'>
      {Object.entries(config.auths).map(([server, auth], index) => {
        let username = String(auth.username ?? '')
        let password = String(auth.password ?? '')
        if (!username && !password && typeof auth.auth === 'string') {
          try {
            const decoded = new TextDecoder().decode(
              Uint8Array.from(atob(auth.auth), (c) => c.charCodeAt(0))
            )
            const colon = decoded.indexOf(':')
            if (colon >= 0) {
              username = decoded.slice(0, colon)
              password = decoded.slice(colon + 1)
            }
          } catch {
            /* The raw data layout remains available for invalid auth. */
          }
        }
        return (
          <div
            key={index}
            className='grid gap-4 border-b border-[#ebeef5] pb-5 last:border-0'
          >
            {(
              [
                ['server', 'docker server', server],
                ['username', '用户名', username],
                ['password', '密码', password],
              ] as const
            ).map(([field, label, value]) => (
              <div
                key={field}
                className='grid items-center gap-2 sm:grid-cols-[140px_1fr]'
              >
                <Label htmlFor={`docker-${field}-${index}`}>{label}</Label>
                <div className='flex items-center gap-2'>
                  <Input
                    id={`docker-${field}-${index}`}
                    aria-label={`${label} ${index + 1}`}
                    readOnly={!editing}
                    disabled={disabled}
                    value={value}
                    type={
                      field === 'password' && !showPassword
                        ? 'password'
                        : 'text'
                    }
                    onChange={(e) => {
                      // Materialize auth-only credentials before replacing either field.
                      config.auths[server] = { ...auth, username, password }
                      updateAuth(server, field, e.target.value)
                    }}
                    className='rounded font-mono text-sm'
                  />
                  {field === 'password' && (
                    <Button
                      variant='ghost'
                      size='icon'
                      aria-label={showPassword ? '隐藏密码' : '显示密码'}
                      onClick={() => setShowPassword((v) => !v)}
                    >
                      {showPassword ? (
                        <EyeOff className='size-4' />
                      ) : (
                        <Eye className='size-4' />
                      )}
                    </Button>
                  )}
                </div>
              </div>
            ))}
          </div>
        )
      })}
      {Object.keys(config.auths).length === 0 && (
        <p className='text-sm text-[#909399]'>
          暂无镜像仓库凭据，请在原始数据中添加 auths 条目。
        </p>
      )}
    </div>
  )
}
