import {
  DataResourceDialog,
  type DataResourceDialogProps,
} from './data-resource-dialog'

export function SecretEditDialog(
  props: Omit<DataResourceDialogProps, 'initialMode'>
) {
  return <DataResourceDialog {...props} initialMode='edit' />
}
