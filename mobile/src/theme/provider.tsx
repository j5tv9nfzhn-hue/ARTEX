import * as SecureStore from "expo-secure-store";
import * as React from "react";
import { useColorScheme } from "react-native";

import { palettes, type Palette, type ThemeMode } from "./tokens";

const STORAGE_KEY = "artex.theme.mode";

export type ThemePreference = ThemeMode | "system";

type ThemeContextValue = {
  palette: Palette;
  mode: ThemeMode;
  preference: ThemePreference;
  setPreference: (p: ThemePreference) => void;
};

const ThemeContext = React.createContext<ThemeContextValue>({
  palette: palettes.dark,
  mode: "dark",
  preference: "system",
  setPreference: () => {},
});

export function ThemeProvider({ children }: { children: React.ReactNode }) {
  const system = useColorScheme();
  const [preference, setPreferenceState] = React.useState<ThemePreference>("system");

  React.useEffect(() => {
    let alive = true;
    SecureStore.getItemAsync(STORAGE_KEY)
      .then((value) => {
        if (alive && (value === "light" || value === "dark" || value === "system")) {
          setPreferenceState(value);
        }
      })
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, []);

  const setPreference = React.useCallback((next: ThemePreference) => {
    setPreferenceState(next);
    SecureStore.setItemAsync(STORAGE_KEY, next).catch(() => {});
  }, []);

  const mode: ThemeMode = preference === "system" ? (system === "light" ? "light" : "dark") : preference;

  const value = React.useMemo<ThemeContextValue>(
    () => ({ palette: palettes[mode], mode, preference, setPreference }),
    [mode, preference, setPreference],
  );

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme(): ThemeContextValue {
  return React.useContext(ThemeContext);
}
