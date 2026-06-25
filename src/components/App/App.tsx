import React from 'react';
import { Route, Routes } from 'react-router-dom';
import { AppRootProps } from '@grafana/data';
import { ROUTES } from '../../constants';

const Overview = React.lazy(() => import('../../pages/Overview'));
const Connect = React.lazy(() => import('../../pages/Connect'));
const Activities = React.lazy(() => import('../../pages/Activities'));
const Widgets = React.lazy(() => import('../../pages/Widgets'));

function App(props: AppRootProps) {
  return (
    <Routes>
      <Route path={ROUTES.Connect} element={<Connect meta={props.meta} />} />
      <Route path={ROUTES.Activities} element={<Activities />} />
      <Route path={ROUTES.Widgets} element={<Widgets />} />

      {/* Default page */}
      <Route path="*" element={<Overview />} />
    </Routes>
  );
}

export default App;
