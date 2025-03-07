import MapSearchIcon from 'mdi-react/MapSearchIcon'
import React from 'react'
import { Route, RouteComponentProps, Switch } from 'react-router'

import { LoadingSpinner } from '@sourcegraph/react-loading-spinner'
import { gql, useQuery } from '@sourcegraph/shared/src/graphql/graphql'
import { TelemetryProps } from '@sourcegraph/shared/src/telemetry/telemetryService'
import { ThemeProps } from '@sourcegraph/shared/src/theme'

import { AuthenticatedUser } from '../../auth'
import { withAuthenticatedUser } from '../../auth/withAuthenticatedUser'
import { ErrorBoundary } from '../../components/ErrorBoundary'
import { HeroPage } from '../../components/HeroPage'
import {
    UserAreaUserFields,
    UserSettingsAreaUserFields,
    UserSettingsAreaUserProfileResult,
    UserSettingsAreaUserProfileVariables,
} from '../../graphql-operations'
import { OnboardingTourProps } from '../../search'
import { SiteAdminAlert } from '../../site-admin/SiteAdminAlert'
import { UserExternalServicesOrRepositoriesUpdateProps } from '../../util'
import { RouteDescriptor } from '../../util/contributions'
import { UserAreaRouteContext } from '../area/UserArea'

import { EditUserProfilePageGQLFragment } from './profile/UserSettingsProfilePage'
import { UserSettingsSidebar, UserSettingsSidebarItems } from './UserSettingsSidebar'

const NotFoundPage: React.FunctionComponent = () => <HeroPage icon={MapSearchIcon} title="404: Not Found" />

export interface UserSettingsAreaRoute extends RouteDescriptor<UserSettingsAreaRouteContext> {}

export interface UserSettingsAreaProps
    extends UserAreaRouteContext,
        RouteComponentProps<{}>,
        ThemeProps,
        TelemetryProps,
        OnboardingTourProps,
        UserExternalServicesOrRepositoriesUpdateProps {
    authenticatedUser: AuthenticatedUser
    sideBarItems: UserSettingsSidebarItems
    routes: readonly UserSettingsAreaRoute[]
    user: UserAreaUserFields
}

export interface UserSettingsAreaRouteContext extends UserSettingsAreaProps {
    user: UserSettingsAreaUserFields
}

const UserSettingsAreaGQLFragment = gql`
    fragment UserSettingsAreaUserFields on User {
        __typename
        id
        username
        displayName
        url
        settingsURL
        avatarURL
        viewerCanAdminister
        siteAdmin @include(if: $siteAdmin)
        builtinAuth
        createdAt
        emails @include(if: $siteAdmin) {
            email
            verified
        }
        organizations {
            nodes {
                id
                displayName
                name
            }
        }
        tags @include(if: $siteAdmin)
        ...EditUserProfilePage
    }
    ${EditUserProfilePageGQLFragment}
`

const USER_SETTINGS_AREA_USER_PROFILE = gql`
    query UserSettingsAreaUserProfile($userID: ID!, $siteAdmin: Boolean!) {
        node(id: $userID) {
            __typename
            ...UserSettingsAreaUserFields
        }
    }
    ${UserSettingsAreaGQLFragment}
`

/**
 * Renders a layout of a sidebar and a content area to display user settings.
 */
export const AuthenticatedUserSettingsArea: React.FunctionComponent<UserSettingsAreaProps> = props => {
    const { authenticatedUser, sideBarItems } = props

    const { data, error, loading, previousData } = useQuery<
        UserSettingsAreaUserProfileResult,
        UserSettingsAreaUserProfileVariables
    >(USER_SETTINGS_AREA_USER_PROFILE, {
        variables: {
            userID: props.user.id,
            siteAdmin: authenticatedUser.siteAdmin,
        },
    })

    // Accept stale data if recently updated, avoids unmounting components due to a brief lack of data
    const user =
        (data?.node?.__typename === 'User' && data?.node) ||
        (previousData?.node?.__typename === 'User' && previousData?.node)

    if (loading && !user) {
        return null
    }

    if (error) {
        throw new Error(error.message)
    }

    if (!user) {
        return <NotFoundPage />
    }

    if (authenticatedUser.id !== user.id && !user.viewerCanAdminister) {
        return (
            <HeroPage
                icon={MapSearchIcon}
                title="403: Forbidden"
                subtitle="You are not authorized to view or edit this user's settings."
            />
        )
    }

    const context: UserSettingsAreaRouteContext = {
        ...props,
        user,
    }

    const siteAdminViewingOtherUser = user.id !== authenticatedUser.id

    return (
        <>
            {/* Indicate when the site admin is viewing another user's account */}
            {siteAdminViewingOtherUser && (
                <SiteAdminAlert>
                    Viewing account for <strong>{user.username}</strong>
                </SiteAdminAlert>
            )}
            <div className="d-flex">
                <UserSettingsSidebar items={sideBarItems} {...context} className="flex-0 mr-3 user-settings-sidebar" />
                <div className="flex-1">
                    <ErrorBoundary location={props.location}>
                        <React.Suspense fallback={<LoadingSpinner className="icon-inline m-2" />}>
                            <Switch>
                                {props.routes.map(
                                    ({ path, exact, render, condition = () => true }) =>
                                        condition(context) && (
                                            <Route
                                                render={routeComponentProps =>
                                                    render({ ...context, ...routeComponentProps })
                                                }
                                                path={props.match.url + path}
                                                key="hardcoded-key" // see https://github.com/ReactTraining/react-router/issues/4578#issuecomment-334489490
                                                exact={exact}
                                            />
                                        )
                                )}
                                <Route component={NotFoundPage} key="hardcoded-key" />
                            </Switch>
                        </React.Suspense>
                    </ErrorBoundary>
                </div>
            </div>
        </>
    )
}

export const UserSettingsArea = withAuthenticatedUser(AuthenticatedUserSettingsArea)
