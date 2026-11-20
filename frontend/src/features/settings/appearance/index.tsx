import { ContentSection } from '../components/content-section'
import { AppearanceForm } from './appearance-form'

export function SettingsAppearance() {
  return (
    <ContentSection title='外观' desc='设置界面主题与字体。'>
      <AppearanceForm />
    </ContentSection>
  )
}
