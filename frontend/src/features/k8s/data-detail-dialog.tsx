import {
  DataResourceDialog,
  type DataResourceDialogProps,
} from './data-resource-dialog'

export function DataDetailDialog(
  props: DataResourceDialogProps & { kindLabel: string }
) {
  return <DataResourceDialog {...props} />
}
