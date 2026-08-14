/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.
*/
import { Download, Video } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { IconBadge } from '@/components/ui/icon-badge'

interface VideoPreviewDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  taskId: string
}

export function VideoPreviewDialog({
  open,
  onOpenChange,
  taskId,
}: VideoPreviewDialogProps) {
  const { t } = useTranslation()
  const videoUrl = `/v1/videos/${encodeURIComponent(taskId)}/content`
  const downloadUrl = `${videoUrl}?download=1`

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={
        <>
          <IconBadge tone='chart-4' size='sm'>
            <Video />
          </IconBadge>
          {t('Video Preview')}
        </>
      }
      contentClassName='sm:max-w-3xl'
      titleClassName='flex items-center gap-2'
      contentHeight='auto'
      bodyClassName='space-y-3'
    >
      {open && (
        <video
          src={videoUrl}
          controls
          preload='metadata'
          playsInline
          className='bg-muted aspect-video max-h-[65vh] w-full rounded-md object-contain'
        />
      )}
      <div className='flex justify-end'>
        <Button
          size='sm'
          className='gap-1.5'
          render={<a href={downloadUrl} download />}
        >
          <Download className='size-4' />
          {t('Download video')}
        </Button>
      </div>
    </Dialog>
  )
}
