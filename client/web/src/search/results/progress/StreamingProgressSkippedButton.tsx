import AlertCircleIcon from 'mdi-react/AlertCircleIcon'
import InformationOutlineIcon from 'mdi-react/InformationOutlineIcon'
import React, { useCallback, useMemo, useState } from 'react'
import { ButtonDropdown, DropdownMenu, DropdownToggle } from 'reactstrap'

import { StreamingProgressProps } from './StreamingProgress'
import { StreamingProgressSkippedPopover } from './StreamingProgressSkippedPopover'

export const StreamingProgressSkippedButton: React.FunctionComponent<
    Pick<StreamingProgressProps, 'progress' | 'onSearchAgain'>
> = ({ progress, onSearchAgain }) => {
    const [isOpen, setIsOpen] = useState(false)
    const toggleOpen = useCallback(() => setIsOpen(previous => !previous), [setIsOpen])

    const skippedWithWarningOrError = useMemo(
        () => progress.skipped.some(skipped => skipped.severity === 'warn' || skipped.severity === 'error'),
        [progress]
    )

    const onSearchAgainWithPopupClose = useCallback(
        (filters: string[]) => {
            setIsOpen(false)
            onSearchAgain(filters)
        },
        [setIsOpen, onSearchAgain]
    )

    return (
        <>
            {progress.skipped.length > 0 && (
                <ButtonDropdown isOpen={isOpen} toggle={toggleOpen}>
                    <DropdownToggle
                        className="streaming-progress__skipped mb-0 d-flex align-items-center text-decoration-none btn-sm"
                        caret={true}
                        color={skippedWithWarningOrError ? 'outline-danger' : 'outline-secondary'}
                    >
                        {skippedWithWarningOrError ? (
                            <AlertCircleIcon className="mr-2 icon-inline" />
                        ) : (
                            <InformationOutlineIcon className="mr-2 icon-inline" />
                        )}
                        Some results excluded
                    </DropdownToggle>
                    <DropdownMenu className="streaming-progress__skipped-popover">
                        <StreamingProgressSkippedPopover
                            progress={progress}
                            onSearchAgain={onSearchAgainWithPopupClose}
                        />
                    </DropdownMenu>
                </ButtonDropdown>
            )}
        </>
    )
}
