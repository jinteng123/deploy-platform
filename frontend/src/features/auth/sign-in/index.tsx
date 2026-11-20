import { useSearch } from '@tanstack/react-router'
import { AuthLayout } from '../auth-layout'
import { UserAuthForm } from './components/user-auth-form'

export function SignIn() {
  const { redirect } = useSearch({ from: '/(auth)/sign-in' })

  return (
    <AuthLayout>
      <div className='rounded-2xl bg-white p-8 shadow-[0_1px_2px_rgba(16,40,80,0.04),0_12px_32px_rgba(37,76,132,0.08)] ring-1 ring-[#e3eaf4] dark:bg-card dark:ring-border'>
        <h1 className='text-2xl font-semibold tracking-tight text-[#1c355e]'>
          欢迎登录
        </h1>
        <p className='mt-2 text-sm text-muted-foreground'>
          使用管理员分配的账号密码登录平台。
        </p>
        <UserAuthForm className='mt-8' redirectTo={redirect} />
      </div>
      <p className='mt-6 text-center text-xs text-muted-foreground'>
        忘记密码？请联系平台管理员重置
      </p>
    </AuthLayout>
  )
}
