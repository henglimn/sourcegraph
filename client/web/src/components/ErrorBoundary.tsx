import * as sentry from '@sentry/browser'
import * as H from 'history'
import ErrorIcon from 'mdi-react/ErrorIcon'
import ReloadIcon from 'mdi-react/ReloadIcon'
import React from 'react'

import { HTTPStatusError } from '@sourcegraph/shared/src/backend/fetch'
import { asError, isErrorLike } from '@sourcegraph/shared/src/util/errors'

import { HeroPage } from './HeroPage'

interface Props {
    /**
     * The current location, or null if there is no location (such as the root component, which is above the
     * react-router component).
     */
    location: H.Location | null

    /**
     * Extra context to aid with debugging
     */
    extraContext?: JSX.Element

    /**
     * Custom render logic in place of <HeroPage>
     */
    render?: (error: Error) => JSX.Element

    /**
     * Classname to pass to <HeroPage>
     */
    className?: string
}

interface State {
    error?: Error
}

/**
 * A [React error boundary](https://reactjs.org/docs/error-boundaries.html) that catches errors from
 * its children. If an error occurs, it displays a nice error page instead of a blank page and reports the error to Sentry.
 *
 * Components should handle their own errors (and must not rely on this error boundary). This error
 * boundary is a last resort in case of an unexpected error.
 */
export class ErrorBoundary extends React.PureComponent<Props, State> {
    public state: State = {}

    public static getDerivedStateFromError(error: any): Pick<State, 'error'> {
        return { error: asError(error) }
    }

    public componentDidCatch(error: unknown, errorInfo: React.ErrorInfo): void {
        if (shouldErrorBeReported(error)) {
            sentry.withScope(scope => {
                for (const [key, value] of Object.entries(errorInfo)) {
                    scope.setExtra(key, value)
                }
                sentry.captureException(error)
            })
        }
    }

    public componentDidUpdate(previousProps: Props): void {
        if (previousProps.location !== this.props.location) {
            // Reset error state when location changes, so that the user can try navigating to a different page to
            // clear the error.
            /* eslint react/no-did-update-set-state: warn */
            this.setState({ error: undefined })
        }
    }

    public render(): React.ReactNode | null {
        if (this.state.error !== undefined) {
            if (isWebpackChunkError(this.state.error)) {
                // "Loading chunk 123 failed" means that the JavaScript assets that correspond to the deploy
                // version currently running are no longer available, likely because a redeploy occurred after the
                // user initially loaded this page.
                return (
                    <HeroPage
                        icon={ReloadIcon}
                        title="Reload required"
                        subtitle={
                            <div className="container">
                                <p>A new version of Sourcegraph is available.</p>
                                <button type="button" className="btn btn-primary" onClick={this.onReloadClick}>
                                    Reload to update
                                </button>
                            </div>
                        }
                    />
                )
            }

            if (this.props.render) {
                return this.props.render(this.state.error)
            }

            return (
                <HeroPage
                    icon={ErrorIcon}
                    title="Error"
                    className={this.props.className}
                    subtitle={
                        <div className="container">
                            <p>
                                Sourcegraph encountered an unexpected error. If reloading the page doesn't fix it,
                                contact your site admin or Sourcegraph support.
                            </p>
                            <p>
                                <code className="text-wrap">{this.state.error.message}</code>
                            </p>
                            {this.props.extraContext}
                        </div>
                    }
                />
            )
        }

        return this.props.children
    }

    private onReloadClick: React.MouseEventHandler<HTMLElement> = () => {
        window.location.reload() // hard page reload
    }
}

function shouldErrorBeReported(error: unknown): boolean {
    if (error instanceof HTTPStatusError) {
        // Ignore Server error responses (5xx)
        return error.status < 500
    }

    return true
}

function isWebpackChunkError(value: unknown): boolean {
    return isErrorLike(value) && value.name === 'ChunkLoadError'
}
